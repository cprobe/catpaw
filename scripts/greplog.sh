#!/bin/sh
#
# exec.toml example:
# [[instances]]
# commands = [
#     "/opt/catpaw/scripts/greplog.sh 'Out of memory'"
# ]

if [ "$1" ]; then
    keyword=$1
else
    keyword="Out of memory"
fi

output=$(journalctl -S-3m | grep "${keyword}")
status="Ok"
if [ -n "$output" ]; then
    status="Warning"
fi

echo '[
    {
        "event_status": "'${status}'",
        "labels": {
            "check": "grep keyword: '${keyword}'"
        },
        "title_rule": "$check",
        "description": "'${output}'"
    }
]'
