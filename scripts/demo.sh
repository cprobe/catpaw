#!/bin/sh

echo '[
    {
        "event_status": "Warning",
        "labels": {
            "check": "script demo"
        },
        "title_rule": "$check",
        "description": "this is description, support markdown"
    }
]'