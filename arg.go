package myai

import "github.com/tidwall/gjson"

type Args struct {
	s string
}

func NewArgs(s string) *Args {
	return &Args{s: s}
}

func (args *Args) OriginalArgument() string {
	return args.s
}

func (args *Args) Get(key string) gjson.Result {
	return gjson.Get(args.s, key)
}

func (args *Args) GetStrings(key string) []string {
	array := gjson.Get(args.s, key).Array()
	var res []string
	for _, v := range array {
		res = append(res, v.String())
	}
	return res
}
