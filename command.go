package main

import (
	"context"
	"flag"
	"time"

	"github.com/posener/complete"
)

type Command struct {
	Name      string
	UsageLine string
	UsageLong string
	Run       func(ctx *context.Context, cmd *Command, cfg interface{}, args []string)
	Flags     []Flag
	Args      complete.Predictor
}

type Flag struct {
	Name      string
	Predictor complete.Predictor
	FlagType  int
	Default   interface{}
	Usage     string
}

const (
	FlagTypeInt      = 1
	FlagTypeString   = 2
	FlagTypeDuration = 3
	FlagTypeBool     = 4
)

type BindFlag struct {
	Name string
	Val  interface{}
}

func (cmd *Command) BindFlagSet(bindFlags ...BindFlag) *flag.FlagSet {
	fs := flag.NewFlagSet(cmd.Name, flag.ExitOnError)

	for _, bind := range bindFlags {
		var fdef *Flag
		for _, x := range cmd.Flags {
			if x.Name == bind.Name {
				fdef = &x
				break
			}
		}
		if fdef == nil {
			panic("attempt to bind invalid flag: " + bind.Name)
		}
		switch fdef.FlagType {
		case FlagTypeInt:
			fs.IntVar(bind.Val.(*int), fdef.Name, fdef.Default.(int), fdef.Usage)
		case FlagTypeString:
			fs.StringVar(bind.Val.(*string), fdef.Name, fdef.Default.(string), fdef.Usage)
		case FlagTypeDuration:
			fs.DurationVar(bind.Val.(*time.Duration), fdef.Name, fdef.Default.(time.Duration), fdef.Usage)
		case FlagTypeBool:
			fs.BoolVar(bind.Val.(*bool), fdef.Name, fdef.Default.(bool), fdef.Usage)
		}
	}
	return fs
}

func (cmd *Command) CompleteFlags() complete.Flags {
	cf := make(complete.Flags)
	for _, fl := range cmd.Flags {
		cf["-"+fl.Name] = fl.Predictor
	}
	return cf
}
func (cmd *Command) CompleteCommand() complete.Command {
	return complete.Command{
		Args:  cmd.Args,
		Flags: cmd.CompleteFlags(),
	}
}
