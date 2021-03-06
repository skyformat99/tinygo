#!/usr/bin/python3

import sys
import os
from xml.dom import minidom
from glob import glob
from collections import OrderedDict
import re

ARM_ARCHS = {
    'CM0': 'armv6m',
    'CM4': 'armv7em',
}

class Device:
    # dummy
    pass

def getText(element):
    strings = []
    for node in element.childNodes:
        if node.nodeType == node.TEXT_NODE:
            strings.append(node.data)
    return ''.join(strings)

def formatText(text):
    text = re.sub('[ \t\n]+', ' ', text) # Collapse whitespace (like in HTML)
    text = text.replace('\\n ', '\n')
    text = text.strip()
    return text

def readSVD(path):
    # Read ARM SVD files.
    device = Device()
    xml = minidom.parse(path)
    root = xml.getElementsByTagName('device')[0]
    deviceName = getText(root.getElementsByTagName('name')[0])
    deviceDescription = getText(root.getElementsByTagName('description')[0])
    licenseText = formatText(getText(root.getElementsByTagName('licenseText')[0]))
    cpu = root.getElementsByTagName('cpu')[0]
    cpuName = getText(cpu.getElementsByTagName('name')[0])

    device.peripherals = []

    interrupts = OrderedDict()

    for periphEl in root.getElementsByTagName('peripherals')[0].getElementsByTagName('peripheral'):
        name = getText(periphEl.getElementsByTagName('name')[0])
        description = getText(periphEl.getElementsByTagName('description')[0])
        baseAddress = int(getText(periphEl.getElementsByTagName('baseAddress')[0]), 0)

        peripheral = {
            'name':        name,
            'description': description,
            'baseAddress': baseAddress,
            'registers':   [],
        }
        device.peripherals.append(peripheral)

        for interrupt in periphEl.getElementsByTagName('interrupt'):
            intrName = getText(interrupt.getElementsByTagName('name')[0])
            intrIndex = int(getText(interrupt.getElementsByTagName('value')[0]))
            if intrName in interrupts:
                if interrupts[intrName]['index'] != intrIndex:
                    raise ValueError('interrupt with the same name has different indexes: ' + intrName)
                interrupts[intrName]['description'] += ' // ' + description
            else:
                interrupts[intrName] = {
                    'name':        intrName,
                    'index':       intrIndex,
                    'description': description,
                }

        regsEls = periphEl.getElementsByTagName('registers')
        if regsEls:
            for el in regsEls[0].childNodes:
                if el.nodeName == 'register':
                    peripheral['registers'].append(parseSVDRegister(name, el, baseAddress))
                elif el.nodeName == 'cluster':
                    if el.getElementsByTagName('dim'):
                        continue # TODO
                    clusterPrefix = getText(el.getElementsByTagName('name')[0]) + '_'
                    clusterOffset = int(getText(el.getElementsByTagName('addressOffset')[0]), 0)
                    for regEl in el.childNodes:
                        if regEl.nodeName == 'register':
                            peripheral['registers'].append(parseSVDRegister(name, regEl, baseAddress + clusterOffset, clusterPrefix))
                else:
                    continue

    device.interrupts = interrupts.values() # TODO: sort by index
    device.metadata = {
        'file':             os.path.basename(path),
        'descriptorSource': 'https://github.com/NordicSemiconductor/nrfx/tree/master/mdk',
        'name':             deviceName,
        'nameLower':        deviceName.lower(),
        'description':      deviceDescription,
        'licenseBlock':     '\n//     ' + licenseText.replace('\n', '\n//     '),
        'arch':             ARM_ARCHS[cpuName],
        'family':           getText(root.getElementsByTagName('series')[0]),
    }

    return device

def parseSVDRegister(peripheralName, regEl, baseAddress, namePrefix=''):
    regName = getText(regEl.getElementsByTagName('name')[0])
    regDescription = getText(regEl.getElementsByTagName('description')[0])
    offsetEls = regEl.getElementsByTagName('offset')
    if not offsetEls:
        offsetEls = regEl.getElementsByTagName('addressOffset')
    address = baseAddress + int(getText(offsetEls[0]), 0)

    dimEls = regEl.getElementsByTagName('dim')
    array = None
    if dimEls:
        array = int(getText(dimEls[0]), 0)
        regName = regName.replace('[%s]', '')

    fields = []
    fieldsEls = regEl.getElementsByTagName('fields')
    if fieldsEls:
        for fieldEl in fieldsEls[0].childNodes:
            if fieldEl.nodeName != 'field':
                continue
            fieldName = getText(fieldEl.getElementsByTagName('name')[0])
            descrEls = fieldEl.getElementsByTagName('description')
            lsb = int(getText(fieldEl.getElementsByTagName('lsb')[0]))
            msb = int(getText(fieldEl.getElementsByTagName('msb')[0]))
            fields.append({
                'name':        '{}_{}{}_{}_Pos'.format(peripheralName, namePrefix, regName, fieldName),
                'description': 'Position of %s field.' % fieldName,
                'value':       lsb,
            })
            fields.append({
                'name':        '{}_{}{}_{}_Msk'.format(peripheralName, namePrefix, regName, fieldName),
                'description': 'Bit mask of %s field.' % fieldName,
                'value':       (0xffffffff >> (31 - (msb - lsb))) << lsb,
            })
            for enumEl in fieldEl.getElementsByTagName('enumeratedValue'):
                enumName = getText(enumEl.getElementsByTagName('name')[0])
                enumDescription = getText(enumEl.getElementsByTagName('description')[0])
                enumValue = int(getText(enumEl.getElementsByTagName('value')[0]), 0)
                fields.append({
                    'name':        '{}_{}{}_{}_{}'.format(peripheralName, namePrefix, regName, fieldName, enumName),
                    'description': enumDescription,
                    'value':       enumValue,
                })

    return {
        'name':    namePrefix + regName,
        'address': address,
        'description': regDescription.replace('\n', ' '),
        'bitfields':   fields,
        'array':       array,
    }

def writeGo(outdir, device):
    # The Go module for this device.
    out = open(outdir + '/' + device.metadata['nameLower'] + '.go', 'w')
    pkgName = os.path.basename(outdir.rstrip('/'))
    out.write('''\
// Automatically generated file. DO NOT EDIT.
// Generated by gen-device.py from {file}, see {descriptorSource}

// +build {pkgName},{nameLower}

// {description}
// {licenseBlock}
package {pkgName}

import "unsafe"

// Magic type name for the compiler.
type __volatile uint32

// Export this magic type name.
type RegValue = __volatile

// Some information about this device.
const (
	DEVICE     = "{name}"
	ARCH       = "{arch}"
	FAMILY     = "{family}"
)
'''.format(pkgName=pkgName, **device.metadata))

    out.write('\n// Interrupts\nconst (\n')
    for intr in device.interrupts:
        out.write('\tIRQ_{name} = {index} // {description}\n'.format(**intr))
    intrMax = max(map(lambda intr: intr['index'], device.interrupts))
    out.write('\tIRQ_max = {} // Highest interrupt number on this device.\n'.format(intrMax))
    out.write(')\n')

    for peripheral in device.peripherals:
        out.write('\n// {description}\ntype {name}_Type struct {{\n'.format(**peripheral))
        address = peripheral['baseAddress']
        padNumber = 0
        for register in peripheral['registers']:
            if address > register['address']:
                # In Nordic SVD files, these registers are deprecated or
                # duplicates, so can be ignored.
                #print('skip: %s.%s' % (peripheral['name'], register['name']))
                continue

            # insert padding, if needed
            if address < register['address']:
                numSkip = (register['address'] - address) // 4
                if numSkip == 1:
                    out.write('\t_padding{padNumber} __volatile\n'.format(padNumber=padNumber))
                else:
                    out.write('\t_padding{padNumber} [{num}]__volatile\n'.format(padNumber=padNumber, num=numSkip))
                padNumber += 1

            regType = '__volatile'
            if register['array'] is not None:
                regType = '[{}]__volatile'.format(register['array'])
            out.write('\t{name} {regType}\n'.format(**register, regType=regType))

            # next address
            if register['array'] is not None and 1:
                address = register['address'] + 4 * register['array']
            else:
                address = register['address'] + 4
        out.write('}\n')

    out.write('\n// Peripherals.\nvar (\n')
    for peripheral in device.peripherals:
        out.write('\t{name} = (*{name}_Type)(unsafe.Pointer(uintptr(0x{baseAddress:x}))) // {description}\n'.format(**peripheral))
    out.write(')\n')

    for peripheral in device.peripherals:
        if not sum(map(lambda r: len(r['bitfields']), peripheral['registers'])): continue
        out.write('\n// Bitfields for {name}: {description}\nconst('.format(**peripheral))
        for register in peripheral['registers']:
            if not register['bitfields']: continue
            out.write('\n\t// {name}'.format(**register))
            if register['description']:
                out.write(': {description}'.format(**register))
            out.write('\n')
            for bitfield in register['bitfields']:
                out.write('\t{name} = 0x{value:x}'.format(**bitfield))
                if bitfield['description']:
                    out.write(' // {description}'.format(**bitfield))
                out.write('\n')
        out.write(')\n')


def generate(indir, outdir):
    for filepath in sorted(glob(indir + '/*.svd')):
        print(filepath)
        device = readSVD(filepath)
        writeGo(outdir, device)


if __name__ == '__main__':
    indir = sys.argv[1] # directory with register descriptor files (*.svd, *.atdf)
    outdir = sys.argv[2] # output directory
    generate(indir, outdir)
