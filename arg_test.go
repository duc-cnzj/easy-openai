package myai

import (
	"testing"
)

func TestNewArgs(t *testing.T) {
	args := NewArgs(`{"name": ["duc", "abc"], "age": 1}`)
	value := args.Get("name")
	for _, v := range value.Array() {
		t.Log(v.String())
	}
}
