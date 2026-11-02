package jara

import (
	"encoding/json"
	"os"
	"os/exec"
	"strings"
)

func showHostDiagnostics() error {
	binary, err := os.Executable()
	if err != nil {
		return err
	}
	command := exec.Command(binary, "_rpc", token(Request{Op: "doctor"}))
	output, runErr := command.Output()
	var diagnosis Diagnosis
	if err = json.Unmarshal(output, &diagnosis); err != nil {
		if runErr != nil {
			return runErr
		}
		return err
	}
	rows := make([]menuItem, 0, len(diagnosis.Checks))
	for _, check := range diagnosis.Checks {
		title, detail, _ := strings.Cut(check, ":")
		rows = append(rows, menuItem{Title: title, Detail: strings.TrimSpace(detail), Kind: "tools"})
	}
	_, err = selectMenu("Diagnostics", rows)
	return err
}
