package main

import (
	"flag"
	"fmt"
	"os"
	"text/tabwriter"
)

func cmdTemplates(args []string) error {
	if len(args) == 0 {
		return fmt.Errorf("用法: cardex templates init|status|refresh [-root ROOT] [NAME...]")
	}
	switch args[0] {
	case "init":
		return cmdTemplatesInit(args[1:])
	case "status":
		return cmdTemplatesStatus(args[1:])
	case "refresh":
		return cmdTemplatesRefresh(args[1:])
	default:
		return fmt.Errorf("未知 templates 子命令 %s", args[0])
	}
}

func cmdTemplatesInit(args []string) error {
	fs := flag.NewFlagSet("templates init", flag.ContinueOnError)
	rootFlag := fs.String("root", "", "数据目录")
	if err := fs.Parse(args); err != nil {
		return err
	}
	root := resolveRoot(*rootFlag)
	if err := writeDefaultTemplates(root); err != nil {
		return err
	}
	fmt.Printf("templates initialized: %s\n", templatesDir(root))
	return nil
}

func cmdTemplatesStatus(args []string) error {
	fs := flag.NewFlagSet("templates status", flag.ContinueOnError)
	rootFlag := fs.String("root", "", "数据目录")
	if err := fs.Parse(args); err != nil {
		return err
	}
	root := resolveRoot(*rootFlag)
	rows, err := listTemplateStatuses(root)
	if err != nil {
		return err
	}
	w := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
	fmt.Fprintln(w, "NAME\tSOURCE\tDIFFERENCE")
	for _, row := range rows {
		fmt.Fprintf(w, "%s\t%s\t%s\n", row.Name, row.Source, row.Difference)
	}
	return w.Flush()
}

func cmdTemplatesRefresh(args []string) error {
	fs := flag.NewFlagSet("templates refresh", flag.ContinueOnError)
	rootFlag := fs.String("root", "", "数据目录")
	if err := fs.Parse(args); err != nil {
		return err
	}
	root := resolveRoot(*rootFlag)
	results, err := refreshSelectedTemplates(root, fs.Args())
	if err != nil {
		return err
	}
	for _, r := range results {
		if r.BackupPath != "" {
			fmt.Printf("refreshed %s backup=%s\n", r.Name, r.BackupPath)
			continue
		}
		fmt.Printf("refreshed %s\n", r.Name)
	}
	return nil
}
