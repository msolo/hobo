package cmdflag

import (
	"context"
	"flag"
	"fmt"
	"os"
	"time"

	"github.com/posener/complete"
)

var (
	PredictNothing  = complete.PredictNothing
	PredictAnything = complete.PredictAnything
	PredictDirs     = complete.PredictDirs
	PredictFiles    = complete.PredictFiles
	PredictFilesSet = complete.PredictFilesSet
	PredictSet      = complete.PredictSet
	PredictOr       = complete.PredictOr
)

type Command struct {
	Name      string
	UsageLine string
	UsageLong string
	Run       func(ctx context.Context, cmd *Command, args []string)
	Flags     []Flag
	Args      complete.Predictor
	flagSet   *flag.FlagSet
}

type Flag struct {
	Name         string
	FlagType     int
	DefaultValue interface{}
	Usage        string
	Predictor    complete.Predictor
}

const (
	FlagTypeInt = iota
	FlagTypeString
	FlagTypeDuration
	FlagTypeBool
)

func (cmd *Command) BindFlagSet(bindFlags map[string]interface{}) *flag.FlagSet {
	if cmd.flagSet != nil {
		panic("flag set already bound for command: " + cmd.Name)
	}
	fs := flag.NewFlagSet(cmd.Name, flag.ExitOnError)
	fs.Usage = func() {
		fmt.Fprintf(os.Stderr, "Usage of %s:\n  %s\n", cmd.Name, cmd.UsageLine)
		fmt.Fprintln(os.Stderr, cmd.UsageLong)
		fs.PrintDefaults()
	}

	for name, val := range bindFlags {
		var fdef *Flag
		for _, x := range cmd.Flags {
			if x.Name == name {
				fdef = &x
				break
			}
		}
		if fdef == nil {
			panic("attempt to bind invalid flag: " + name)
		}
		switch fdef.FlagType {
		case FlagTypeInt:
			fs.IntVar(val.(*int), fdef.Name, fdef.DefaultValue.(int), fdef.Usage)
		case FlagTypeString:
			fs.StringVar(val.(*string), fdef.Name, fdef.DefaultValue.(string), fdef.Usage)
		case FlagTypeDuration:
			fs.DurationVar(val.(*time.Duration), fdef.Name, fdef.DefaultValue.(time.Duration), fdef.Usage)
		case FlagTypeBool:
			fs.BoolVar(val.(*bool), fdef.Name, fdef.DefaultValue.(bool), fdef.Usage)
		}
	}
	cmd.flagSet = fs
	return fs
}

func (cmd *Command) FlagSet() *flag.FlagSet {
	if cmd.flagSet == nil {
		cmd.BindFlagSet(nil)
	}
	return cmd.flagSet
}

func (cmd *Command) completeFlags() complete.Flags {
	cf := make(complete.Flags)
	for _, fl := range cmd.Flags {
		cf["-"+fl.Name] = fl.Predictor
	}
	return cf
}

func (cmd *Command) completeCommand() complete.Command {
	return complete.Command{
		Args:  cmd.Args,
		Flags: cmd.completeFlags(),
	}
}

func Parse(cmdMain *Command, subCmds []*Command) (cmd *Command, args []string) {
	cmdModeMap := make(map[string]*Command)
	cmplModeMap := make(complete.Commands)
	for _, cmd := range subCmds {
		cmdModeMap[cmd.Name] = cmd
		cmplModeMap[cmd.Name] = cmd.completeCommand()
	}

	cmplMain := complete.Command{
		Sub:   cmplModeMap,
		Flags: cmdMain.completeFlags(),
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
