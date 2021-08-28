package server

import (
	"fmt"
	"testing"
)

func TestGo1(t *testing.T) {
	ch := make(chan struct{}, 1000)
	ch <- struct{}{}
	fmt.Println(len(ch))
	<-ch
	fmt.Println(len(ch))
}
