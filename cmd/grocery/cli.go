package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"strings"

	toon "github.com/toon-format/toon-go"

	"github.com/jgalea/grocery-cli/internal/registry"
	"github.com/jgalea/grocery-cli/internal/store"
)

var stderrLogf = func(format string, args ...any) {
	fmt.Fprintf(os.Stderr, "grocery: "+format+"\n", args...)
}

// common flags shared by every subcommand.
type common struct {
	store   string
	lang    string
	jsonOut bool
	toon    bool
}

func newCommonFlags(name string) (*flag.FlagSet, *common) {
	fs := flag.NewFlagSet(name, flag.ExitOnError)
	c := &common{}
	fs.StringVar(&c.store, "store", "", "store key (or GROCERY_STORE; default: "+registry.Default+")")
	fs.StringVar(&c.lang, "lang", "", "catalog language (store-specific)")
	fs.BoolVar(&c.jsonOut, "json", false, "emit raw JSON to stdout")
	fs.BoolVar(&c.toon, "toon", false, "emit TOON to stdout")
	return fs, c
}

// newStore resolves the selected store (flag > env > default) into an adapter.
func newStore(c *common) (store.Store, error) {
	key := firstNonEmpty(c.store, os.Getenv("GROCERY_STORE"), registry.Default)
	return registry.Get(key, c.lang, stderrLogf)
}

func parseFlags(fs *flag.FlagSet, args []string) { _ = fs.Parse(reorderArgs(fs, args)) }

// reorderArgs hoists flags (and their values) ahead of a "--" terminator so they
// parse even when placed after positionals.
func reorderArgs(fs *flag.FlagSet, args []string) []string {
	var flags, positional []string
	for i := 0; i < len(args); i++ {
		a := args[i]
		if a == "--" {
			positional = append(positional, args[i+1:]...)
			break
		}
		if len(a) > 1 && a[0] == '-' && (a[1] < '0' || a[1] > '9') && a[1] != '.' {
			flags = append(flags, a)
			name := strings.TrimLeft(a, "-")
			if strings.IndexByte(name, '=') >= 0 {
				continue
			}
			if f := fs.Lookup(name); f != nil && !isBoolFlag(f) && i+1 < len(args) {
				flags = append(flags, args[i+1])
				i++
			}
			continue
		}
		positional = append(positional, a)
	}
	out := make([]string, 0, len(flags)+1+len(positional))
	out = append(out, flags...)
	out = append(out, "--")
	return append(out, positional...)
}

func isBoolFlag(f *flag.Flag) bool {
	bf, ok := f.Value.(interface{ IsBoolFlag() bool })
	return ok && bf.IsBoolFlag()
}

func firstNonEmpty(vals ...string) string {
	for _, v := range vals {
		if v != "" {
			return v
		}
	}
	return ""
}

func emitJSON(v any) error {
	enc := json.NewEncoder(os.Stdout)
	enc.SetIndent("", "  ")
	enc.SetEscapeHTML(false)
	return enc.Encode(v)
}

func emitTOON(v any) error {
	raw, err := json.Marshal(v)
	if err != nil {
		return err
	}
	var generic any
	_ = json.Unmarshal(raw, &generic)
	s, err := toon.MarshalString(generic)
	if err != nil || s == "" {
		return err
	}
	_, err = fmt.Fprintln(os.Stdout, s)
	return err
}

func emitStructured(cf *common, v any) (bool, error) {
	switch {
	case cf.toon:
		return true, emitTOON(v)
	case cf.jsonOut:
		return true, emitJSON(v)
	}
	return false, nil
}
