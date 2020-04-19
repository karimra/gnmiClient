package cmd

import (
	"context"
	"fmt"
	"os"
	"strings"

	"github.com/google/gnxi/utils/xpath"
	"github.com/manifoldco/promptui"
	"github.com/spf13/viper"
)

func selectFromList(lsName string, items []string, initialPos, pageSize int) (int, string, error) {
	if pageSize <= 0 {
		pageSize = 10
	}
	nl := make([]string, len(items)+1)
	nl[0] = ".."
	copy(nl[1:], items)
	p := promptui.Select{
		Label:        lsName,
		Items:        nl,
		Size:         pageSize,
		CursorPos:    initialPos,
		Stdout:       os.Stderr,
		HideSelected: true,
		Searcher: func(input string, index int) bool {
			kws := strings.Split(input, " ")
			result := true
			for _, kw := range kws {
				if strings.HasPrefix(kw, "!") {
					kw = strings.TrimLeft(kw, "!")
					if kw == "" {
						continue
					}
					result = result && !strings.Contains(nl[index], kw)
				} else {
					result = result && strings.Contains(nl[index], kw)
				}
			}
			return result
		},
		Keys: &promptui.SelectKeys{
			Prev:     promptui.Key{Code: promptui.KeyPrev, Display: promptui.KeyPrevDisplay},
			Next:     promptui.Key{Code: promptui.KeyNext, Display: promptui.KeyNextDisplay},
			PageUp:   promptui.Key{Code: promptui.KeyBackward, Display: promptui.KeyBackwardDisplay},
			PageDown: promptui.Key{Code: promptui.KeyForward, Display: promptui.KeyForwardDisplay},
			Search:   promptui.Key{Code: ':', Display: ":"},
		},
	}

	pos, selected, err := p.Run()
	if err != nil {
		return 0, "", err
	}
	return pos, selected, nil
}

func selectManyFromList(lsName string, items []string, pageSize int) ([]string, error) {
	result := make([]string, 0)
	choice := ""
	var err error
	pos := 0
	nl := make([]string, len(items)+1)
	nl[0] = ".."
	copy(nl[1:], items)
	numSelected := 0
	p := promptui.Select{
		//Label:        fmt.Sprintf("%s (selected:%d)", lsName, numSelected),
		Items:        nl,
		Size:         pageSize,
		CursorPos:    pos,
		Stdout:       os.Stderr,
		HideSelected: true,
		Searcher: func(input string, index int) bool {
			kws := strings.Split(input, " ")
			result := true
			for _, kw := range kws {
				if strings.HasPrefix(kw, "!") {
					kw = strings.TrimLeft(kw, "!")
					if kw == "" {
						continue
					}
					result = result && !strings.Contains(nl[index], kw)
				} else {
					result = result && strings.Contains(nl[index], kw)
				}
			}
			return result
		},
		Keys: &promptui.SelectKeys{
			Prev:     promptui.Key{Code: promptui.KeyPrev, Display: promptui.KeyPrevDisplay},
			Next:     promptui.Key{Code: promptui.KeyNext, Display: promptui.KeyNextDisplay},
			PageUp:   promptui.Key{Code: promptui.KeyBackward, Display: promptui.KeyBackwardDisplay},
			PageDown: promptui.Key{Code: promptui.KeyForward, Display: promptui.KeyForwardDisplay},
			Search:   promptui.Key{Code: ':', Display: ":"},
		},
	}
LOOP:
	p.Label = fmt.Sprintf("%s (selected:%d)", lsName, numSelected)
	_, choice, err = p.Run()
	if err != nil {
		return nil, err
	}
	if choice == ".." {
		return result, nil
	}
	//p.CursorPos = pos
	for _, r := range result {
		if r == choice {
			goto LOOP
		}
	}
	numSelected++
	result = append(result, choice)
	goto LOOP
}

func selectManyWithAddFromList(lsName string, items []string) ([]string, error) {
	result := make([]string, 0)
	choice := ""
	var err error
	nl := make([]string, len(items)+2)
	copy(nl, []string{"ALL", ".."})
	copy(nl[2:], items)
	numSelected := 0
	p := promptui.SelectWithAdd{
		Label:    fmt.Sprintf("%s (selected:%d)", lsName, numSelected),
		Items:    nl,
		AddLabel: "Other:",
	}
LOOP:
	p.Label = fmt.Sprintf("%s (selected:%d)", lsName, numSelected)
	_, choice, err = p.Run()
	if err != nil {
		return nil, err
	}
	if choice == ".." {
		return result, nil
	}
	if choice == "ALL" {
		return items, nil
	}
	for _, r := range result {
		if r == choice {
			goto LOOP
		}
	}
	numSelected++
	result = append(result, choice)
	goto LOOP
}

func selectTargets(addrs []string) ([]string, error) {
	var err error
	addrs, err = selectManyWithAddFromList("select targets", addrs)
	if err != nil {
		return nil, err
	}
	if len(addrs) == 0 {
		fmt.Println("no grpc server address specified")
		return nil, nil
	}
	if addrs[0] == "ALL" {
		addrs = viper.GetStringSlice("address")
	}
	return addrs, nil
}

func selectPaths() ([]string, error) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	paths, err := getPaths(ctx, viper.GetString("yang-file"), true)
	if err != nil {
		return nil, err
	}
	result, err := selectManyFromList("select paths", paths, 20)
	if err != nil {
		return nil, err
	}
	if len(result) == 0 {
		return nil, fmt.Errorf("no paths selected")
	}
	return result, nil
}

func readFromPrompt(label, defValue string) (string, error) {
	p := promptui.Prompt{
		Label:     label,
		IsConfirm: false,
		Default:   defValue,
	}
	r, err := p.Run()
	if err != nil {
		return "", err
	}
	return r, nil
}

func setPathKeys(p string) (string, error) {
	if !strings.Contains(p, "*") {
		return p, nil
	}
	gnmiPath, err := xpath.ToGNMIPath(p)
	if err != nil {
		return "", err
	}
	for _, pe := range gnmiPath.GetElem() {
		if pe.GetKey() != nil {
			for k := range pe.GetKey() {
				v, err := readFromPrompt(fmt.Sprintf("enter value for %s[%s=*]", pe.GetName(), k), "*")
				if err != nil {
					return "", err
				}
				pe.Key[k] = v
			}
		}
	}
	return gnmiPathToXPath(gnmiPath), nil
}
