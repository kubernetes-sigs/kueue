/*
Copyright 2024 The Kubernetes Authors.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

package generators

import (
	"fmt"
	"html"
	"html/template"
	"io"
	"os"
	"os/user"
	"path/filepath"
	"sort"
	"strings"

	"github.com/spf13/cobra"
	"github.com/spf13/pflag"
)

type Reference struct {
	NoList           bool
	Title            string
	Description      string
	ShowUseLine      bool
	UseLine          string
	ShowExample      bool
	Example          string
	Options          []Option
	InheritedOptions []Option
	Links            []Link
}

type Option struct {
	Name        string
	Shorthand   string
	Usage       string
	ValueType   string
	DefVal      string
	NoOptDefVal string
}

type Link struct {
	Name        string
	Description string
	Path        string
}

type byName []*cobra.Command

func (s byName) Len() int           { return len(s) }
func (s byName) Swap(i, j int)      { s[i], s[j] = s[j], s[i] }
func (s byName) Less(i, j int) bool { return s[i].Name() < s[j].Name() }

func GenMarkdownTree(cmd *cobra.Command, templatesDir, outputDir string) error {
	identity := func(s string) string { return s }
	emptyStr := func(s string) string { return "" }
	return GenMarkdownTreeCustom(cmd, templatesDir, outputDir, "", emptyStr, identity)
}

func GenMarkdownTreeCustom(cmd *cobra.Command, templatesDir, outputDir string, subdir string, filePrepender, linkHandler func(string) string) error {
	for _, c := range cmd.Commands() {
		if !c.IsAvailableCommand() || c.IsAdditionalHelpTopicCommand() {
			continue
		}
		// CommandPath example: 'kjobctl create localqueue'
		parts := strings.Split(c.CommandPath(), " ")
		childSubdir := ""
		if len(parts) > 1 {
			childSubdir = parts[0] + "_" + parts[1]
			fullPath := filepath.Join(outputDir, childSubdir)
			if _, err := os.Stat(fullPath); os.IsNotExist(err) {
				if err := os.Mkdir(fullPath, 0770); err != nil {
					return err
				}
			}
		}
		if err := GenMarkdownTreeCustom(c, templatesDir, outputDir, childSubdir, filePrepender, linkHandler); err != nil {
			return err
		}
	}

	fullname := strings.ReplaceAll(cmd.CommandPath(), " ", "_") + ".md"
	indexFile := false
	if len(subdir) > 0 {
		parts := strings.Split(cmd.CommandPath(), " ")
		if len(parts) == 2 {
			indexFile = true
			fullname = "_index.md"
		}
		fullname = filepath.Join(subdir, fullname)
	}
	filename := filepath.Join(outputDir, fullname)

	f, err := os.Create(filename)
	if err != nil {
		return err
	}
	defer f.Close()

	if _, err := io.WriteString(f, filePrepender(filename)); err != nil {
		return err
	}
	if err := GenReference(cmd, templatesDir, f, linkHandler, indexFile); err != nil {
		return err
	}
	return nil
}

func GenReference(cmd *cobra.Command, templatesDir string, w io.Writer, linkHandler func(string) string, indexFile bool) error {
	ref := getReference(cmd, linkHandler, indexFile)

	tmpl, err := template.New("main.md").
		Funcs(template.FuncMap{
			"html":  htmlFuncMap,
			"usage": usageFuncMap,
		}).
		ParseGlob(fmt.Sprintf("%s/*", templatesDir))
	if err != nil {
		return err
	}
	if err := tmpl.Execute(w, ref); err != nil {
		return err
	}

	return nil
}

func htmlFuncMap(s string) template.HTML {
	return template.HTML(s)
}

func usageFuncMap(usage string) template.HTML {
	usage = html.EscapeString(usage)
	usage = strings.TrimSuffix(usage, "\\n")
	usage = strings.ReplaceAll(usage, "\\n", "<br/>")
	return htmlFuncMap(usage)
}

func getReference(cmd *cobra.Command, linkHandler func(string) string, indexFile bool) *Reference {
	cmd.InitDefaultHelpCmd()
	cmd.InitDefaultHelpFlag()

	ref := &Reference{
		NoList:           indexFile,
		Title:            strings.TrimSpace(cmd.CommandPath()),
		Description:      cmd.Long,
		Example:          cmd.Example,
		Options:          getOptions(cmd.NonInheritedFlags()),
		InheritedOptions: getOptions(cmd.InheritedFlags()),
		Links:            getLinks(cmd, linkHandler, indexFile),
	}

	if len(ref.Description) == 0 {
		ref.Description = cmd.Short
	}

	if cmd.Runnable() {
		ref.UseLine = cmd.UseLine()
	}

	return ref
}

func getOptions(f *pflag.FlagSet) []Option {
	var options []Option

	f.VisitAll(func(flag *pflag.Flag) {
		if len(flag.Deprecated) > 0 || flag.Hidden {
			return
		}

		option := Option{
			Name:        flag.Name,
			Usage:       flag.Usage,
			ValueType:   getValueType(flag),
			DefVal:      getDefaultVal(flag),
			NoOptDefVal: getNoOptDefVal(flag),
		}

		if len(flag.ShorthandDeprecated) == 0 {
			option.Shorthand = flag.Shorthand
		}

		options = append(options, option)
	})

	return options
}

func getValueType(flag *pflag.Flag) string {
	name := flag.Value.Type()
	switch name {
	case "bool":
		return ""
	case "float64", "float32":
		return "float"
	case "int64", "int32", "int8":
		return "int"
	case "uint64", "uint32", "uint8":
		return "uint"
	case "stringSlice", "stringArray":
		return "strings"
	case "float32Slice", "float64Slice":
		return "floats"
	case "intSlice", "int32Slice", "int64Slice":
		return "ints"
	case "uintSlice":
		return "uints"
	case "boolSlice":
		return "bools"
	case "durationSlice":
		return "durations"
	case "stringToString":
		return "<comma-separated 'key=value' pairs>"
	case "stringToInt":
		return "<comma-separated 'key=int' pairs>"
	default:
		return name
	}
}

func getNoOptDefVal(flag *pflag.Flag) string {
	if len(flag.NoOptDefVal) == 0 {
		return ""
	}

	switch flag.Value.Type() {
	case "string":
		return fmt.Sprintf("[=\"%s\"]", flag.NoOptDefVal)
	case "bool":
		if flag.NoOptDefVal != "true" {
			return fmt.Sprintf("[=%s]", flag.NoOptDefVal)
		} else {
			return ""
		}
	default:
		return fmt.Sprintf("[=%s]", flag.NoOptDefVal)
	}
}

func getDefaultVal(flag *pflag.Flag) string {
	var defaultVal string

	if !defaultIsZeroValue(flag) {
		defaultVal = flag.DefValue
		switch flag.Value.Type() {
		case "string":
			// There are cases where the string is very-very long, split
			// it to mutiple lines manually
			if len(defaultVal) > 40 {
				defaultVal = strings.ReplaceAll(defaultVal, ",", ",<br />")
			}
			// clean up kjobctl cache-dir flag value
			if flag.Name == "cache-dir" {
				myUser, err := user.Current()
				if err == nil {
					noprefix := strings.TrimPrefix(defaultVal, myUser.HomeDir)
					defaultVal = fmt.Sprintf("$HOME%s", noprefix)
				}
			}
			defaultVal = fmt.Sprintf("\"%s\"", defaultVal)
		case "stringSlice":
			// For string slices, the default value should not contain '[' ]r ']'
			defaultVal = strings.TrimPrefix(defaultVal, "[")
			defaultVal = strings.TrimSuffix(defaultVal, "]")
			defaultVal = strings.ReplaceAll(defaultVal, " ", "")
			defaultVal = fmt.Sprintf("\"%s\"", defaultVal)
		}
	}

	return defaultVal
}

func getLinks(cmd *cobra.Command, linkHandler func(string) string, indexFile bool) []Link {
	var links []Link

	if cmd.HasParent() {
		parent := cmd.Parent()

		parts := strings.Split(parent.CommandPath(), " ")
		commandPath := strings.Join(parts, "_")

		var path string
		if indexFile {
			path = "../" + commandPath + ".md"
		} else {
			path = "_index.md"
		}

		link := Link{
			Name:        commandPath,
			Path:        linkHandler(path),
			Description: parent.Short,
		}

		cmd.VisitParents(func(c *cobra.Command) {
			if c.DisableAutoGenTag {
				cmd.DisableAutoGenTag = c.DisableAutoGenTag
			}
		})

		links = append(links, link)
	}

	name := cmd.CommandPath()
	children := cmd.Commands()
	sort.Sort(byName(children))

	for _, child := range children {
		if !child.IsAvailableCommand() || child.IsAdditionalHelpTopicCommand() {
			continue
		}
		cname := name + " " + child.Name()
		path := strings.ReplaceAll(cname, " ", "_")
		if indexFile {
			path += ".md"
		} else {
			path += "/_index.md"
		}

		link := Link{
			Name:        cname,
			Path:        linkHandler(path),
			Description: child.Short,
		}

		links = append(links, link)
	}

	return links
}

func defaultIsZeroValue(f *pflag.Flag) bool {
	switch f.Value.Type() {
	case "bool":
		return f.DefValue == "false"
	case "duration":
		return f.DefValue == "0" || f.DefValue == "0s"
	case "int", "int8", "int32", "int64", "uint", "uint8", "uint16", "uint32", "count", "float32", "float64":
		return f.DefValue == "0"
	case "string":
		return f.DefValue == ""
	case "ip", "ipMask", "ipNet":
		return f.DefValue == "<nil>"
	case "intSlice", "stringSlice", "stringArray":
		return f.DefValue == "[]"
	case "namedCertKey":
		return f.DefValue == "[]"
	default:
		switch f.Value.String() {
		case "false":
			return true
		case "<nil>":
			return true
		case "":
			return true
		case "0":
			return true
		}
		return false
	}
}
