#!/usr/bin/env bash
# PostToolUse hook: keep the model working when a bash command fails.
#
# Crush's bash tool reports a non-zero exit status as a *successful* tool
# call whose output carries a trailing "Exit code N" line, so command
# failures surface on PostToolUse rather than PostToolUseFailure. When
# that line is present we emit a continuation telling the model to
# diagnose the error and keep fixing it.
#
# options.hook_continuation_limit (5 by default) caps how many
# continuations a single turn may receive; past the cap Crush replaces
# this context with an instruction to stop retrying.
#
# Anything else stays silent: no context, no decision, no opinion.

input=$(cat)
response=$(printf '%s' "$input" | jq -r '.tool_response // empty')

# formatOutput appends the exit code on its own line, e.g. "Exit code 1".
if printf '%s' "$response" | grep -qE '^Exit code [1-9][0-9]*$'; then
	jq -nc '{
		context: "上一条命令执行失败，请分析错误并继续修复，最多尝试 5 次。",
		continuation: true
	}'
fi
