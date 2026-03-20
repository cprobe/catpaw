package aiclient

// CompactMessages removes the oldest conversation rounds to fit within a
// token budget. It always keeps messages[0] (the system prompt). Tool-call
// sequences (an assistant message with ToolCalls followed by its tool
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

	groups := parseMessageGroups(messages)
	systemTokens := EstimateMessageTokens(messages[0])
	budget := tokenBudget - systemTokens
	if budget <= 0 {
		return messages[:1]
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
		return messages[:1]
	}

	result := make([]Message, 0, len(messages)-groups[keepFrom].startIdx+1)
	result = append(result, messages[0])
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
	tokens := EstimateTokensChinese(m.Content) + perMessageOverhead
	for _, tc := range m.ToolCalls {
		tokens += EstimateTokensChinese(tc.Function.Name)
		tokens += EstimateTokensChinese(tc.Function.Arguments)
		tokens += 10
	}
	return tokens
}

func parseMessageGroups(messages []Message) []messageGroup {
	var groups []messageGroup
	i := 1
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
