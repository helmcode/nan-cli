package tools

import "testing"

const sweepWindow = 1048576

func TestCodexWindowsPrunedLeavesMultilineStringsAlone(t *testing.T) {
	for _, q := range []string{`"""`, `'''`} {
		content := "model_context_window = 1048576 # nan-cli\n" +
			"instructions = " + q + "\nline one\nmodel_context_window = 1 # nan-cli\nmodel_context_window = 1048576\n" + q + "\n"
		out, changed := codexContextWindowsPruned([]byte(content), sweepWindow)
		if changed || string(out) != content {
			t.Errorf("%s: string was edited: changed=%v\n%s", q, changed, out)
		}
		if _, ok := codexTOML(string(out)); !ok {
			t.Errorf("%s: result does not parse", q)
		}
	}
}

func TestCodexWindowsPrunedIgnoresBracketLinesInStrings(t *testing.T) {
	content := "model_context_window = 1048576 # nan-cli\n" +
		"instructions = \"\"\"\n[guide](https://x)\nmodel_context_window = 1 # nan-cli\n\"\"\"\n"
	out, changed := codexContextWindowsPruned([]byte(content), sweepWindow)
	if changed || string(out) != content {
		t.Errorf("string was edited: changed=%v\n%s", changed, out)
	}
}

func TestCodexWindowsBracketLineInStringDoesNotHideRootDuplicate(t *testing.T) {
	content := "model_context_window = 1048576 # nan-cli\n" +
		"instructions = \"\"\"\n[guide](https://x)\n\"\"\"\nmodel_context_window = 1048576 # nan-cli\n"
	out, changed := codexContextWindowsPruned([]byte(content), sweepWindow)
	want := "model_context_window = 1048576 # nan-cli\n" +
		"instructions = \"\"\"\n[guide](https://x)\n\"\"\"\n"
	if !changed || string(out) != want {
		t.Errorf("duplicate kept: changed=%v\n%s", changed, out)
	}
}

func TestCodexWindowsClearedLeavesMultilineStringsAlone(t *testing.T) {
	content := "model_context_window = 1048576 # nan-cli\n\n" +
		"instructions = \"\"\"\nmodel_context_window = 1 # nan-cli\n\nmodel_context_window = 1048576\n\"\"\"\n"
	out, changed := codexContextWindowsCleared([]byte(content), sweepWindow)
	want := "\ninstructions = \"\"\"\nmodel_context_window = 1 # nan-cli\n\nmodel_context_window = 1048576\n\"\"\"\n"
	if !changed || string(out) != want {
		t.Errorf("changed=%v\n%s", changed, out)
	}
	if _, ok := codexTOML(string(out)); !ok {
		t.Error("result does not parse")
	}
}

func TestCodexWindowsClearedOnlyLookalikeInStringIsUntouched(t *testing.T) {
	content := "instructions = '''\nmodel_context_window = 1 # nan-cli\n'''\n"
	out, changed := codexContextWindowsCleared([]byte(content), sweepWindow)
	if changed || string(out) != content {
		t.Errorf("changed=%v\n%s", changed, out)
	}
}

func TestCodexWindowsPrunedStillRepairsDuplicates(t *testing.T) {
	content := "model_context_window = 1048576 # nan-cli\n" +
		"model_context_window = 1048576 # nan-cli\n" +
		"model_context_window = 1048576\n\n[projects.\"/a\"]\ntrust_level = \"trusted\"\n"
	if _, ok := codexTOML(content); ok {
		t.Fatal("fixture should not parse")
	}
	out, changed := codexContextWindowsPruned([]byte(content), sweepWindow)
	want := "model_context_window = 1048576 # nan-cli\n\n[projects.\"/a\"]\ntrust_level = \"trusted\"\n"
	if !changed || string(out) != want {
		t.Errorf("changed=%v\n%s", changed, out)
	}
}

func TestCodexWindowsDuplicateRepairKeepsLookalikeStringLine(t *testing.T) {
	content := "model_context_window = 1048576 # nan-cli\n" +
		"instructions = \"\"\"\nmodel_context_window = 1 # nan-cli\n\"\"\"\n" +
		"model_context_window = 1048576 # nan-cli\n"
	out, changed := codexContextWindowsPruned([]byte(content), sweepWindow)
	want := "model_context_window = 1048576 # nan-cli\n" +
		"instructions = \"\"\"\nmodel_context_window = 1 # nan-cli\n\"\"\"\n"
	if !changed || string(out) != want {
		t.Errorf("changed=%v\n%s", changed, out)
	}
	if _, ok := codexTOML(string(out)); !ok {
		t.Error("result does not parse")
	}
}

func TestCodexWindowsWindowValueOpeningAStringIsLeftAlone(t *testing.T) {
	content := "[projects.\"/a\"]\nmodel_context_window = \"\"\" # nan-cli\nfoo\n\"\"\"\n"
	if _, ok := codexTOML(content); !ok {
		t.Fatal("fixture should parse")
	}
	for name, sweep := range map[string]func([]byte, int) ([]byte, bool){
		"pruned": codexContextWindowsPruned, "cleared": codexContextWindowsCleared,
	} {
		out, changed := sweep([]byte(content), sweepWindow)
		if changed || string(out) != content {
			t.Errorf("%s: changed=%v\n%s", name, changed, out)
		}
	}
}

// Valid TOML whose window value is an array: dropping the first line alone
// would leave the rest of it behind, so the re-parse has to refuse the edit.
func TestCodexWindowsEditThatChangesMoreThanWindowsIsRefused(t *testing.T) {
	content := "model_context_window = [ # nan-cli\n1,\n]\n"
	if _, ok := codexTOML(content); !ok {
		t.Fatal("fixture should parse")
	}
	out, changed := codexContextWindowsCleared([]byte(content), sweepWindow)
	if changed || string(out) != content {
		t.Errorf("changed=%v\n%s", changed, out)
	}
}

func TestCodexWindowsRepairThatStaysInvalidIsRefused(t *testing.T) {
	for name, content := range map[string]string{
		"unrelated error": "x = \nmodel_context_window = 1048576 # nan-cli\nmodel_context_window = 1048576 # nan-cli\n",
		"string opener":   "model_context_window = 1048576 # nan-cli\nmodel_context_window = \"\"\" # nan-cli\nx\n\"\"\"\n",
	} {
		out, changed := codexContextWindowsPruned([]byte(content), sweepWindow)
		if changed || string(out) != content {
			t.Errorf("%s: changed=%v\n%s", name, changed, out)
		}
	}
	// A remove on a file that is invalid for another reason keeps the window.
	content := "x = \nmodel_context_window = 1048576 # nan-cli\n"
	out, changed := codexContextWindowsCleared([]byte(content), sweepWindow)
	if changed || string(out) != content {
		t.Errorf("clear: changed=%v\n%s", changed, out)
	}
}

// Dropping the opener of a string in a file that is already broken could
// turn the string's body into keys and make the file parse as something else.
func TestCodexWindowsRepairNeverDropsAStringOpener(t *testing.T) {
	content := "model_context_window = 1048576\nmodel_context_window = \"\"\"\nx = \"\"\"\n\"\"\""
	out, changed := codexContextWindowsPruned([]byte(content), sweepWindow)
	if changed || string(out) != content {
		t.Errorf("changed=%v\n%s", changed, out)
	}
}

func TestCodexWindowsSweepHandlesCRLF(t *testing.T) {
	content := "model_context_window = 1048576 # nan-cli\r\ninstructions = \"\"\"\r\nmodel_context_window = 1 # nan-cli\r\n\"\"\"\r\nmodel_context_window = 1048576 # nan-cli\r\n"
	out, changed := codexContextWindowsPruned([]byte(content), sweepWindow)
	want := "model_context_window = 1048576 # nan-cli\r\ninstructions = \"\"\"\r\nmodel_context_window = 1 # nan-cli\r\n\"\"\"\r\n"
	if !changed || string(out) != want {
		t.Errorf("changed=%v\n%q", changed, out)
	}
}
