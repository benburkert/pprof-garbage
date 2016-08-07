package garbage

import (
	"os"
	"testing"
	"time"
)

func TestGarbage(t *testing.T) {
	go genGarbage()
	go lessGarbage()
	go notGarbage()

	WriteGarbageProfile(os.Stdout, 10*time.Second, true)
}

func genGarbage() {
	for {
		bytes := make([]byte, 10<<20)
		for i := range bytes {
			bytes[i] = byte(i)
		}
		time.Sleep(10 * time.Microsecond)
	}
}

func lessGarbage() {
	for {
		bytes := make([]byte, 1<<20)
		for i := range bytes {
			bytes[i] = byte(i)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

var hold = make([][]byte, 0, 1<<20)

func notGarbage() {
	for {
		bytes := make([]byte, 1<<20)
		for i := range bytes {
			bytes[i] = byte(i)
		}
		hold = append(hold, bytes)

		time.Sleep(1 * time.Millisecond)
	}
}
