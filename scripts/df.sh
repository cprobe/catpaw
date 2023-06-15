#!/bin/sh
# use plugin sfilter

output=`df -hT`
count=`echo "$output" | grep -c '100%'`
if [ $count -gt 0 ]; then
    echo $output
fi