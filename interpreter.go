package main

// This file provides functionality to interpret very basic Go SSA, for
// compile-time initialization of globals.

import (
	"errors"
	"go/constant"
	"go/token"
	"go/types"
	"strings"

	"golang.org/x/tools/go/ssa"
)

// Interpret instructions as far as possible, and drop those instructions from
// the basic block.
func (p *Program) Interpret(block *ssa.BasicBlock) error {
	for {
		i, err := p.interpret(block.Instrs, nil, nil, nil)
		if err == cgoWrapperError {
			// skip this instruction
			block.Instrs = block.Instrs[i+1:]
			continue
		}
		block.Instrs = block.Instrs[i:]
		return err
	}
}

// Interpret instructions as far as possible, and return the index of the first
// unknown instruction.
func (p *Program) interpret(instrs []ssa.Instruction, paramKeys []*ssa.Parameter, paramValues []Value, results []Value) (int, error) {
	locals := map[ssa.Value]Value{}
	for i, key := range paramKeys {
		locals[key] = paramValues[i]
	}
	for i, instr := range instrs {
		switch instr := instr.(type) {
		case *ssa.Alloc:
			alloc, err := p.getZeroValue(instr.Type().Underlying().(*types.Pointer).Elem())
			if err != nil {
				return i, err
			}
			locals[instr] = &PointerValue{nil, &alloc}
		case *ssa.BinOp:
			if typ, ok := instr.Type().(*types.Basic); ok && typ.Kind() == types.String {
				// Concatenate two strings.
				// This happens in the time package, for example.
				x, err := p.getValue(instr.X, locals)
				if err != nil {
					return i, err
				}
				y, err := p.getValue(instr.Y, locals)
				if err != nil {
					return i, err
				}
				xstr := constant.StringVal(x.(*ConstValue).Expr.Value)
				ystr := constant.StringVal(y.(*ConstValue).Expr.Value)
				locals[instr] = &ConstValue{ssa.NewConst(constant.MakeString(xstr+ystr), types.Typ[types.String])}
			} else {
				return i, errors.New("init: unknown binop: " + instr.String())
			}
		case *ssa.Call:
			common := instr.Common()
			callee := common.StaticCallee()
			if callee == nil {
				return i, nil // don't understand dynamic dispatch
			}
			if callee.String() == "syscall.runtime_envs" {
				// TODO: replace this with some //go:linkname magic.
				// For now, do as if it returned a zero-length slice.
				var err error
				locals[instr], err = p.getZeroValue(callee.Signature.Results().At(0).Type())
				if err != nil {
					return i, err
				}
				continue
			}
			if canInterpret(callee) {
				params := make([]Value, len(common.Args))
				for i, arg := range common.Args {
					val, err := p.getValue(arg, locals)
					if err != nil {
						return i, err
					}
					params[i] = val
				}
				results := make([]Value, callee.Signature.Results().Len())
				subi, err := p.interpret(callee.Blocks[0].Instrs, callee.Params, params, results)
				if err != nil {
					return i, err
				}
				if subi != len(callee.Blocks[0].Instrs) {
					return i, errors.New("init: could not interpret all instructions of subroutine")
				}
				if len(results) == 1 {
					locals[instr] = results[0]
				} else {
					panic("unimplemented: not exactly 1 result")
				}
				continue
			}
			if callee.Object() == nil || callee.Object().Name() == "init" {
				return i, nil // arrived at the init#num functions
			}
			return i, errors.New("todo: init call: " + callee.String())
		case *ssa.Convert:
			x, err := p.getValue(instr.X, locals)
			if err != nil {
				return i, err
			}
			switch typ := instr.Type().Underlying().(type) {
			case *types.Basic:
				if _, ok := instr.X.Type().Underlying().(*types.Pointer); ok && typ.Kind() == types.UnsafePointer {
					locals[instr] = &PointerBitCastValue{typ, x}
				} else if xtyp, ok := instr.X.Type().Underlying().(*types.Basic); ok && xtyp.Kind() == types.UnsafePointer && typ.Kind() == types.Uintptr {
					locals[instr] = &PointerToUintptrValue{x}
				} else {
					return i, errors.New("todo: init: unknown basic convert: " + instr.String())
				}
			case *types.Pointer:
				if xtyp, ok := instr.X.Type().Underlying().(*types.Basic); ok && xtyp.Kind() == types.UnsafePointer {
					locals[instr] = &PointerBitCastValue{typ, x}
				} else {
					panic("expected unsafe pointer conversion")
				}
			default:
				return i, errors.New("todo: init: unknown convert: " + instr.String())
			}
		case *ssa.DebugRef:
			// ignore
		case *ssa.FieldAddr:
			x, err := p.getValue(instr.X, locals)
			if err != nil {
				return i, err
			}
			var structVal *StructValue
			switch x := x.(type) {
			case *GlobalValue:
				structVal = x.Global.initializer.(*StructValue)
			case *PointerValue:
				structVal = (*x.Elem).(*StructValue)
			default:
				panic("expected a pointer")
			}
			locals[instr] = &PointerValue{nil, &structVal.Fields[instr.Field]}
		case *ssa.IndexAddr:
			x, err := p.getValue(instr.X, locals)
			if err != nil {
				return i, err
			}
			if cnst, ok := instr.Index.(*ssa.Const); ok {
				index, _ := constant.Int64Val(cnst.Value)
				switch xPtr := x.(type) {
				case *GlobalValue:
					x = xPtr.Global.initializer
				case *PointerValue:
					x = *xPtr.Elem
				default:
					panic("expected a pointer")
				}
				switch x := x.(type) {
				case *ArrayValue:
					locals[instr] = &PointerValue{nil, &x.Elems[index]}
				default:
					return i, errors.New("todo: init IndexAddr not on an array or struct")
				}
			} else {
				return i, errors.New("todo: init IndexAddr index: " + instr.Index.String())
			}
		case *ssa.MakeInterface:
			locals[instr] = &InterfaceValue{instr.X.Type(), locals[instr.X]}
		case *ssa.MakeMap:
			locals[instr] = &MapValue{instr.Type().Underlying().(*types.Map), nil, nil}
		case *ssa.MapUpdate:
			// Assume no duplicate keys exist. This is most likely true for
			// autogenerated code, but may not be true when trying to interpret
			// user code.
			key, err := p.getValue(instr.Key, locals)
			if err != nil {
				return i, err
			}
			value, err := p.getValue(instr.Value, locals)
			if err != nil {
				return i, err
			}
			x := locals[instr.Map].(*MapValue)
			x.Keys = append(x.Keys, key)
			x.Values = append(x.Values, value)
		case *ssa.Return:
			for i, r := range instr.Results {
				val, err := p.getValue(r, locals)
				if err != nil {
					return i, err
				}
				results[i] = val
			}
		case *ssa.Slice:
			// Turn a just-allocated array into a slice.
			if instr.Low != nil || instr.High != nil || instr.Max != nil {
				return i, errors.New("init: slice expression with bounds")
			}
			source, err := p.getValue(instr.X, locals)
			if err != nil {
				return i, err
			}
			switch source := source.(type) {
			case *PointerValue: // pointer to array
				array := (*source.Elem).(*ArrayValue)
				locals[instr] = &SliceValue{instr.Type().Underlying().(*types.Slice), array}
			default:
				return i, errors.New("init: unknown slice type")
			}
		case *ssa.Store:
			if addr, ok := instr.Addr.(*ssa.Global); ok {
				if strings.HasPrefix(instr.Addr.Name(), "__cgofn__cgo_") || strings.HasPrefix(instr.Addr.Name(), "_cgo_") {
					// Ignore CGo global variables which we don't use.
					continue
				}
				value, err := p.getValue(instr.Val, locals)
				if err != nil {
					return i, err
				}
				p.GetGlobal(addr).initializer = value
			} else if addr, ok := locals[instr.Addr]; ok {
				value, err := p.getValue(instr.Val, locals)
				if err != nil {
					return i, err
				}
				if addr, ok := addr.(*PointerValue); ok {
					*(addr.Elem) = value
				} else {
					panic("store to non-pointer")
				}
			} else {
				return i, errors.New("todo: init Store: " + instr.String())
			}
		case *ssa.UnOp:
			if instr.Op != token.MUL || instr.CommaOk {
				return i, errors.New("init: unknown unop: " + instr.String())
			}
			valPtr, err := p.getValue(instr.X, locals)
			if err != nil {
				return i, err
			}
			switch valPtr := valPtr.(type) {
			case *GlobalValue:
				locals[instr] = valPtr.Global.initializer
			case *PointerValue:
				locals[instr] = *valPtr.Elem
			default:
				panic("expected a pointer")
			}
		default:
			return i, nil
		}
	}
	return len(instrs), nil
}

// Check whether this function can be interpreted at compile time. For that, it
// needs to only contain relatively simple instructions (for example, no control
// flow).
func canInterpret(callee *ssa.Function) bool {
	if len(callee.Blocks) != 1 || callee.Signature.Results().Len() != 1 {
		// No control flow supported so only one basic block.
		// Only exactly one return value supported right now so check that as
		// well.
		return false
	}
	for _, instr := range callee.Blocks[0].Instrs {
		switch instr.(type) {
		// Ignore all functions fully supported by Program.interpret()
		// above.
		case *ssa.Alloc:
		case *ssa.Convert:
		case *ssa.DebugRef:
		case *ssa.FieldAddr:
		case *ssa.IndexAddr:
		case *ssa.MakeInterface:
		case *ssa.MakeMap:
		case *ssa.MapUpdate:
		case *ssa.Return:
		case *ssa.Slice:
		case *ssa.Store:
		case *ssa.UnOp:
		default:
			return false
		}
	}
	return true
}

func (p *Program) getValue(value ssa.Value, locals map[ssa.Value]Value) (Value, error) {
	switch value := value.(type) {
	case *ssa.Const:
		return &ConstValue{value}, nil
	case *ssa.Function:
		return &FunctionValue{value.Type(), value}, nil
	case *ssa.Global:
		if strings.HasPrefix(value.Name(), "__cgofn__cgo_") || strings.HasPrefix(value.Name(), "_cgo_") {
			// Ignore CGo global variables which we don't use.
			return nil, cgoWrapperError
		}
		g := p.GetGlobal(value)
		if g.initializer == nil {
			value, err := p.getZeroValue(value.Type().Underlying().(*types.Pointer).Elem())
			if err != nil {
				return nil, err
			}
			g.initializer = value
		}
		return &GlobalValue{g}, nil
	default:
		if local, ok := locals[value]; ok {
			return local, nil
		} else {
			return nil, errors.New("todo: init: unknown value: " + value.String())
		}
	}
}

func (p *Program) getZeroValue(t types.Type) (Value, error) {
	switch typ := t.Underlying().(type) {
	case *types.Array:
		elems := make([]Value, typ.Len())
		for i := range elems {
			elem, err := p.getZeroValue(typ.Elem())
			if err != nil {
				return nil, err
			}
			elems[i] = elem
		}
		return &ArrayValue{typ.Elem(), elems}, nil
	case *types.Basic:
		return &ZeroBasicValue{typ}, nil
	case *types.Signature:
		return &FunctionValue{typ, nil}, nil
	case *types.Interface:
		return &InterfaceValue{typ, nil}, nil
	case *types.Map:
		return &MapValue{typ, nil, nil}, nil
	case *types.Pointer:
		return &PointerValue{typ, nil}, nil
	case *types.Struct:
		elems := make([]Value, typ.NumFields())
		for i := range elems {
			elem, err := p.getZeroValue(typ.Field(i).Type())
			if err != nil {
				return nil, err
			}
			elems[i] = elem
		}
		return &StructValue{t, elems}, nil
	case *types.Slice:
		return &SliceValue{typ, nil}, nil
	default:
		return nil, errors.New("todo: init: unknown global type: " + typ.String())
	}
}

// Boxed value for interpreter.
type Value interface {
}

type ConstValue struct {
	Expr *ssa.Const
}

type ZeroBasicValue struct {
	Type *types.Basic
}

type PointerValue struct {
	Type types.Type
	Elem *Value
}

type FunctionValue struct {
	Type types.Type
	Elem *ssa.Function
}

type InterfaceValue struct {
	Type types.Type
	Elem Value
}

type PointerBitCastValue struct {
	Type types.Type
	Elem Value
}

type PointerToUintptrValue struct {
	Elem Value
}

type GlobalValue struct {
	Global *Global
}

type ArrayValue struct {
	ElemType types.Type
	Elems    []Value
}

type StructValue struct {
	Type   types.Type // types.Struct or types.Named
	Fields []Value
}

type SliceValue struct {
	Type  *types.Slice
	Array *ArrayValue
}

type MapValue struct {
	Type   *types.Map
	Keys   []Value
	Values []Value
}
