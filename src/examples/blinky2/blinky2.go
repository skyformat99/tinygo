package main

// This blinky is a bit more advanced than blink1, with two goroutines running
// at the same time and blinking a different LED. The delay of led2 is slightly
// less than half of led1, which would be hard to do without some sort of
// concurrency.

import (
	"machine"
	"runtime"
)

func main() {
	go led1()
	led2()
}

func led1() {
	led := machine.GPIO{machine.LED}
	led.Configure(machine.GPIOConfig{Mode: machine.GPIO_OUTPUT})
	for {
		println("+")
		led.Low()
		runtime.Sleep(runtime.Millisecond * 1000)

		println("-")
		led.High()
		runtime.Sleep(runtime.Millisecond * 1000)
	}
}

func led2() {
	led := machine.GPIO{machine.LED2}
	led.Configure(machine.GPIOConfig{Mode: machine.GPIO_OUTPUT})
	for {
		println("  +")
		led.Low()
		runtime.Sleep(runtime.Millisecond * 420)

		println("  -")
		led.High()
		runtime.Sleep(runtime.Millisecond * 420)
	}
}
