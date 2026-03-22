package aiclient

// CompactMessages removes the oldest conversation rounds to fit within a
// token budget. It always keeps messages[0] (the system prompt). If messages[1]
// is a plain user message (no tool_calls), it is kept as well: some gateways
// reject requests whose tail is only system+assistant+tool without any user
// turn, and diagnosis relies on an initial user kickoff after the system prompt.
// Tool-call sequences (an assistant message with ToolCalls followed by its tool
// responses) are treated as atomic units — kept or removed together.
//
// Returns the original slice unchanged if it already fits within the budget.
func CompactMessages(messages []Message, tokenBudget int) []Message {
	if len(messages) <= 1 || tokenBudget <= 0 {
		return messages
	}

	total := EstimateMessagesTokens(messages)
	if total <= tokenBudget {
		return messages
	}

	stickyPrefix := 1
	if len(messages) > 1 && messages[1].Role == "user" && len(messages[1].ToolCalls) == 0 {
		stickyPrefix = 2
	}
	stickyTokens := EstimateMessagesTokens(messages[:stickyPrefix])
	if stickyTokens > tokenBudget {
		return messages[:stickyPrefix]
	}

	groups := parseMessageGroups(messages, stickyPrefix)
	budget := tokenBudget - stickyTokens
	if budget <= 0 {
		return messages[:stickyPrefix]
	}

	keepFrom := len(groups)
	sum := 0
	for j := len(groups) - 1; j >= 0; j-- {
		if sum+groups[j].tokens > budget {
			break
		}
		sum += groups[j].tokens
		keepFrom = j
	}

	if keepFrom == 0 {
		return messages
	}
	if keepFrom >= len(groups) {
		return messages[:stickyPrefix]
	}

	result := make([]Message, 0, len(messages)-groups[keepFrom].startIdx+stickyPrefix)
	result = append(result, messages[:stickyPrefix]...)
	result = append(result, messages[groups[keepFrom].startIdx:]...)
	return result
}

// EstimateMessagesTokens returns a conservative token estimate for a
// slice of messages, including per-message overhead for role and metadata.
func EstimateMessagesTokens(messages []Message) int {
	total := 0
	for i := range messages {
		total += EstimateMessageTokens(messages[i])
	}
	return total
}

type messageGroup struct {
	startIdx int
	tokens   int
}

const perMessageOverhead = 4

// EstimateMessageTokens returns a conservative token estimate for a single
// message, including role overhead and tool-call structure.
func EstimateMessageTokens(m Message) int {
	tokens := EstimateTokensChinese(m.Content) + EstimateTokensChinese(m.ReasoningContent) + perMessageOverhead
	for _, tc := range m.ToolCalls {
		tokens += EstimateTokensChinese(tc.Function.Name)
		tokens += EstimateTokensChinese(tc.Function.Arguments)
		tokens += 10
	}
	return tokens
}

func parseMessageGroups(messages []Message, startIdx int) []messageGroup {
	var groups []messageGroup
	i := startIdx
	if startIdx < 1 {
		i = 1
	}
	for i < len(messages) {
		g := messageGroup{startIdx: i}
		if messages[i].Role == "assistant" && len(messages[i].ToolCalls) > 0 {
			t := EstimateMessageTokens(messages[i])
			i++
			for i < len(messages) && messages[i].Role == "tool" {
				t += EstimateMessageTokens(messages[i])
				i++
			}
			g.tokens = t
		} else {
			g.tokens = EstimateMessageTokens(messages[i])
			i++
		}
		groups = append(groups, g)
	}
	return groups
}
