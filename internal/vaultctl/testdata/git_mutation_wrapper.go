package main

import (
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
)

func main() {
	command := exec.Command(os.Getenv("VAULTCTL_REAL_GIT"), os.Args[1:]...)
	command.Stdin = os.Stdin
	command.Stdout = os.Stdout
	command.Stderr = os.Stderr
	err := command.Run()
	if err != nil {
		var exitErr *exec.ExitError
		if errors.As(err, &exitErr) {
			os.Exit(exitErr.ExitCode())
		}
		_, _ = os.Stderr.WriteString(err.Error() + "\n")
		os.Exit(1)
	}

	switch os.Getenv("VAULTCTL_GIT_WRAPPER_MODE") {
	case "fetch":
		mutateAfterFetch()
	case "managed-status":
		mutateAfterManagedStatus()
	}
}

func mutateAfterFetch() {
	if !contains(os.Args[1:], "fetch") {
		return
	}
	marker := os.Getenv("VAULTCTL_MUTATION_MARKER")
	if _, err := os.Stat(marker); err == nil || !os.IsNotExist(err) {
		return
	}
	writeMutation()
	if err := os.WriteFile(marker, nil, 0o600); err != nil {
		fatal(err)
	}
}

func mutateAfterManagedStatus() {
	if !contains(os.Args[1:], "--porcelain=v2") || !sameDirectory(mustGetwd(), os.Getenv("VAULTCTL_VAULT_ROOT")) {
		return
	}
	countPath := os.Getenv("VAULTCTL_STATUS_COUNT")
	data, err := os.ReadFile(countPath)
	count := 0
	if err == nil {
		count, err = strconv.Atoi(string(data))
	}
	if err != nil && !os.IsNotExist(err) {
		fatal(err)
	}
	count++
	if err := os.WriteFile(countPath, []byte(strconv.Itoa(count)), 0o600); err != nil {
		fatal(err)
	}
	if count == 2 {
		writeMutation()
	}
}

func writeMutation() {
	if err := os.WriteFile(os.Getenv("VAULTCTL_MUTATION_PATH"), []byte(os.Getenv("VAULTCTL_MUTATION_CONTENT")), 0o644); err != nil {
		fatal(err)
	}
}

func sameDirectory(left, right string) bool {
	left, err := filepath.Abs(left)
	if err != nil {
		fatal(err)
	}
	right, err = filepath.Abs(right)
	if err != nil {
		fatal(err)
	}
	return strings.EqualFold(filepath.Clean(left), filepath.Clean(right))
}

func mustGetwd() string {
	dir, err := os.Getwd()
	if err != nil {
		fatal(err)
	}
	return dir
}

func contains(values []string, wanted string) bool {
	for _, value := range values {
		if value == wanted {
			return true
		}
	}
	return false
}

func fatal(err error) {
	_, _ = os.Stderr.WriteString(err.Error() + "\n")
	os.Exit(1)
}
