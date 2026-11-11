package agent

import (
	"strconv"
	"strings"
)

// unifiedDiff renders a unified diff of two texts (LCS over lines, three
// lines of context). It returns "" when the texts are identical.
func unifiedDiff(oldName, newName, oldText, newText string) string {
	if oldText == newText {
		return ""
	}
	ops := diffLines(strings.Split(oldText, "\n"), strings.Split(newText, "\n"))
	return renderUnifiedDiff(oldName, newName, ops)
}

type diffOp struct {
	tag  byte // ' ' context, '-' removed, '+', added
	line string
}

func diffLines(a, b []string) []diffOp {
	n, m := len(a), len(b)
	// dp[i][j] = length of the LCS of a[i:] and b[j:].
	dp := make([][]int, n+1)
	for i := range dp {
		dp[i] = make([]int, m+1)
	}
	for i := n - 1; i >= 0; i-- {
		row, next := dp[i], dp[i+1]
		for j := m - 1; j >= 0; j-- {
			switch {
			case a[i] == b[j]:
				row[j] = next[j+1] + 1
			case next[j] >= row[j+1]:
				row[j] = next[j]
			default:
				row[j] = row[j+1]
			}
		}
	}

	ops := make([]diffOp, 0, n+m)
	i, j := 0, 0
	for i < n && j < m {
		switch {
		case a[i] == b[j]:
			ops = append(ops, diffOp{' ', a[i]})
			i++
			j++
		case dp[i+1][j] >= dp[i][j+1]:
			ops = append(ops, diffOp{'-', a[i]})
			i++
		default:
			ops = append(ops, diffOp{'+', b[j]})
			j++
		}
	}
	for ; i < n; i++ {
		ops = append(ops, diffOp{'-', a[i]})
	}
	for ; j < m; j++ {
		ops = append(ops, diffOp{'+', b[j]})
	}
	return ops
}

func renderUnifiedDiff(oldName, newName string, ops []diffOp) string {
	const context = 3

	changed := make([]bool, len(ops))
	count := 0
	for i, op := range ops {
		if op.tag != ' ' {
			changed[i] = true
			count++
		}
	}
	if count == 0 {
		return ""
	}

	// Expand each change by the context window and merge overlapping ranges.
	type hunk struct{ start, end int }
	var hunks []hunk
	for i, isChanged := range changed {
		if !isChanged {
			continue
		}
		start := max(i-context, 0)
		end := min(i+context, len(ops)-1)
		if len(hunks) > 0 && start <= hunks[len(hunks)-1].end+1 {
			hunks[len(hunks)-1].end = max(hunks[len(hunks)-1].end, end)
			continue
		}
		hunks = append(hunks, hunk{start: start, end: end})
	}

	var out strings.Builder
	out.WriteString("--- a/" + oldName + "\n")
	out.WriteString("+++ b/" + newName + "\n")
	for _, hunk := range hunks {
		oldStart, oldCount := 1, 0
		newStart, newCount := 1, 0
		for i := 0; i < hunk.start; i++ {
			if ops[i].tag == ' ' || ops[i].tag == '-' {
				oldCount++
			}
			if ops[i].tag == ' ' || ops[i].tag == '+' {
				newCount++
			}
		}
		oldStart += oldCount
		newStart += newCount
		for i := hunk.start; i <= hunk.end; i++ {
			if ops[i].tag == ' ' || ops[i].tag == '-' {
				oldCount++
			}
			if ops[i].tag == ' ' || ops[i].tag == '+' {
				newCount++
			}
		}
		out.WriteString("@@ -" + hunkRange(oldStart, oldCount) + " +" + hunkRange(newStart, newCount) + " @@\n")
		for i := hunk.start; i <= hunk.end; i++ {
			out.WriteByte(ops[i].tag)
			out.WriteString(ops[i].line)
			out.WriteByte('\n')
		}
	}
	return out.String()
}

func hunkRange(start, count int) string {
	if count == 1 {
		return strconv.Itoa(start)
	}
	return strconv.Itoa(start) + "," + strconv.Itoa(count)
}
