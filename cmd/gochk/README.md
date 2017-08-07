# gochk

gochk runs staticcheck, gosimple and unused at once. Because it
is able to reuse work, it will be faster than running each tool
separately.

## Installation

    go get github.com/gm42/go-tools/cmd/gochk

## Usage

The basic operation of gochk is just like that of the other tools.
The flags of the individual tools are prefixed by the tools' names.
Tools can be disabled by setting `-<tool>.enabled=false`.

For explanations of the individual tools, see their respective
READMEs.
