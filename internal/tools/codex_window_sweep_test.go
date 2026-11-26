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
