package server

import (
	"fmt"
	"testing"
)

func TestAnalyzeEditAndMapLine(t *testing.T) {
	source_lines := []string {
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
	target_lines := []string {
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
	var cases = []struct {
		source_lineno int
		expectedOutput string
	}{
		{1, "2"},
		{2, "3"},
		{3, "4"},
		{4, "5"},
		{5, "5"}, // deleted line
		{6, "6"}, // this and the next two lines have been collapsed
		{7, "6"},
		{8, "6"},
		{9, "7"},
		{10, "9"},
	}
	for _, testCase := range cases {
		target_lineno, err := analyzeEditAndMapLine(source_lines, target_lines, testCase.source_lineno)
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
