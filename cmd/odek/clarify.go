package main

import (
	"bufio"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/BackendStack21/odek"
	"github.com/BackendStack21/odek/internal/danger"
	toolpkg "github.com/BackendStack21/odek/internal/tool"
)

// clarifyTimeout is how long a principal-channel question waits before
// the tool returns an error and the loop continues.
const clarifyTimeout = 5 * time.Minute

func ttyClarifyAvailable() bool {
	f, err := os.OpenFile(danger.TTYDevicePath(), os.O_RDWR, 0)
	if err != nil {
		return false
	}
	_ = f.Close()
	return true
}

func appendTTYClarify(tools []odek.Tool) []odek.Tool {
	if !ttyClarifyAvailable() {
		return tools
	}
	return append(tools, toolpkg.NewClarifyTool(ttyClarifyAnswer))
}

func ttyClarifyAnswer(question string) (string, error) {
	var answer string
	err := danger.WithTTYPrompt(func() error {
		tty, err := os.OpenFile(danger.TTYDevicePath(), os.O_RDWR, 0)
		if err != nil {
			return fmt.Errorf("no principal available")
		}
		defer tty.Close()
		fmt.Fprintf(os.Stderr, "\n❓ %s\n   answer: ", question)

		type result struct {
			line string
			err  error
		}
		ch := make(chan result, 1)
		go func() {
			line, err := bufio.NewReader(tty).ReadString('\n')
			ch <- result{line, err}
		}()
		select {
		case r := <-ch:
			if r.err != nil {
				return r.err
			}
			answer = strings.TrimSpace(r.line)
			if answer == "" {
				return fmt.Errorf("empty answer")
			}
			return nil
		case <-time.After(clarifyTimeout):
			return fmt.Errorf("timed out waiting for response")
		}
	})
	return answer, err
}
