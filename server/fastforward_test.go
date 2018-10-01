package server

import (
	"fmt"
	"strconv"
	"testing"
)

func TestAnalyzeEditAndMapLine(t *testing.T) {
	lines0 := []string{
		"func my_function(arg1 int, arg2 int, arg3 string) {",
		"	 if (arg1 == arg2) {",
		"		  log.Print(\"They are the same\")",
		"	 }",
		"	 log.Printf(\"Checked equality\")",
		"	 while (arg1 < arg2) {",
		"		  arg1 += 1",
		"	 }",
		"	 log.Printf(\"Values are %d and %d\", arg1, arg2)",
		"}",
	}
	lines1 := []string{
		"// Comments",
		"func my_method(arg_a int, arg_b int, arg_c string) {",
		"	 if (arg_a == arg_b) {",
		"		  log.Print(\"They are the same\")",
		"	 }",
		"	 while (arg_a < arg_b) { arg_a += 1 }",
		"	 log.Printf(\"Values are %d and %d\", arg_a, arg_b)",
		"	 log.Printf(\"Done!\")",
		"}",
	}
	lines2 := []string{""}

	// Regression test for OOB error in case of a really long patch
	lines3 := append(lines0[:1], append(make([]string, 100), lines0[1:]...)...)

	var cases = []struct {
		source_lines   []string
		target_lines   []string
		source_lineno  int
		expectedOutput string
	}{
		{lines0, lines1, 1, "2"},
		{lines0, lines1, 2, "3"},
		{lines0, lines1, 3, "4"},
		{lines0, lines1, 4, "5"},
		{lines0, lines1, 5, "5"}, // deleted line
		{lines0, lines1, 6, "6"}, // this and the next two lines have been collapsed
		{lines0, lines1, 7, "6"},
		{lines0, lines1, 8, "6"},
		{lines0, lines1, 9, "7"},
		{lines0, lines1, 10, "9"},
		{lines1, lines0, 1, "1"}, // deleted line
		{lines1, lines0, 2, "1"},
		{lines1, lines0, 3, "2"},
		{lines1, lines0, 4, "3"},
		{lines1, lines0, 5, "4"},
		{lines1, lines0, 6, "6"},
		{lines1, lines0, 7, "9"},
		{lines1, lines0, 8, "9"}, // deleted line
		{lines1, lines0, 9, "10"},
		{lines0, lines2, 1, "1"},                                 // regression test
		{lines3, lines0, len(lines3), strconv.Itoa(len(lines0))}, // regression test
	}
	for _, testCase := range cases {
		target_lineno, err := analyzeEditAndMapLine(testCase.source_lines, testCase.target_lines, testCase.source_lineno)
		out := ""
		if err != nil {
			out = fmt.Sprint(err)
		} else {
			out = fmt.Sprint(target_lineno)
		}
		if out != testCase.expectedOutput {
			t.Error("Line", testCase.source_lineno, "failed", "\n  Wanted", testCase.expectedOutput, "\n  Got   ", out)
		}
	}
}
