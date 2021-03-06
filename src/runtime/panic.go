package runtime

// Builtin function panic(msg), used as a compiler intrinsic.
func _panic(message interface{}) {
	printstring("panic: ")
	printitf(message)
	printnl()
	abort()
}

// Cause a runtime panic, which is (currently) always a string.
func runtimePanic(msg string) {
	printstring("panic: runtime error: ")
	println(msg)
	abort()
}

// Check for bounds in *ssa.Index, *ssa.IndexAddr and *ssa.Lookup.
func lookupBoundsCheck(length, index int) {
	if index < 0 || index >= length {
		runtimePanic("index out of range")
	}
}

// Check for bounds in *ssa.Slice.
func sliceBoundsCheck(length, low, high uint) {
	if !(0 <= low && low <= high && high <= length) {
		runtimePanic("slice out of range")
	}
}

// Check for bounds in *ssa.MakeSlice.
func sliceBoundsCheckMake(length, capacity uint) {
	if !(0 <= length && length <= capacity) {
		runtimePanic("slice size out of range")
	}
}
