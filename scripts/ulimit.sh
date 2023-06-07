#!/bin/sh

if [ "$1" ]; then
    threshhold=$1
else
    threshhold=2048
fi

count=$(ulimit -n)

status="Ok"
if [ $count -lt $threshhold ]; then
    status="Warning"
fi

echo '[
    {
        "event_status": "'${status}'",
        "labels": {
            "check": "ulimit check"
        },
        "title_rule": "$check",
        "description": "ulimit -n: '${count}', too low, should be greater than '${threshhold}'"
    }
]'