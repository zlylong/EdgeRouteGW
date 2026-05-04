package main

import (
	"fmt"
	"os/exec"
)

func main() {
	out, _ := exec.Command("host", "-t", "A", "-v", "technews.tw", "127.0.0.1").CombinedOutput()
	fmt.Println(string(out))
}
