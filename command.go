package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"time"

	"github.com/posener/complete"
)

type Command struct {
	Name      string
	UsageLine string
	UsageLong string
	Run       func(ctx context.Context, cmd *Command, args []string)
	Flags     []Flag
	flagSet   *flag.FlagSet
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
	FlagTypeInt = iota
	FlagTypeString
	FlagTypeDuration
	FlagTypeBool
)

type BindFlag struct {
	Name string
	Val  interface{}
}

func (cmd *Command) BindFlagSet(bindFlags ...BindFlag) *flag.FlagSet {
	if cmd.flagSet != nil {
		panic("flag set already bound for command: " + cmd.Name)
	}
	fs := flag.NewFlagSet(cmd.Name, flag.ExitOnError)
	fs.Usage = func() {
		fmt.Fprintf(os.Stderr, "Usage of %s:\n  %s\n", cmd.Name, cmd.UsageLine)
		fmt.Fprintln(os.Stderr, cmd.UsageLong)
		fs.PrintDefaults()
	}

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
	cmd.flagSet = fs
	return fs
}

func (cmd *Command) FlagSet() *flag.FlagSet {
	if cmd.flagSet == nil {
		cmd.BindFlagSet()
	}
	return cmd.flagSet
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

func HandleCommands(cmds []*Command) (cmd *Command, args []string) {
	cmdModeMap := make(map[string]*Command)
	cmplModeMap := make(complete.Commands)
	for _, cmd := range cmds {
		cmdModeMap[cmd.Name] = cmd
		cmplModeMap[cmd.Name] = cmd.CompleteCommand()
	}

	cmdMain := cmds[0]
	cmplMain := complete.Command{
		Sub:   cmplModeMap,
		Flags: cmdMain.CompleteFlags(),
	}

	completer := complete.New(cmdMain.Name, cmplMain)
	if completer.Complete() {
		os.Exit(0)
	}

	flagSet := cmdMain.FlagSet()
	flagSet.Parse(os.Args[1:])

	exitUsage := func() {
		flagSet.Usage()
		os.Exit(1)
	}

	args = flagSet.Args()
	cmdName := ""
	if len(args) > 0 {
		cmdName = args[0]
		args = args[1:]
	}

	if cmd, ok := cmdModeMap[cmdName]; ok {
		return cmd, args
	}

	fmt.Fprintf(os.Stderr, "mode provided but not defined: %s\n", cmdName)
	exitUsage()
	return
}
