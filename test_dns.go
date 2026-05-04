package main

import (
	"fmt"
	"os/exec"
)

func main() {
	out, err := exec.Command("host", "-t", "A", "technews.tw", "127.0.0.1").CombinedOutput()
	fmt.Printf("err: %v\nout: %s\n", err, string(out))
}
