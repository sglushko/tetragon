#!/bin/bash

# Get current PID
CURRENT_PID=$$
echo "1. Original Bash PID: $CURRENT_PID"

# Export it so the next process can see what it was
export EXPECTED_PID=$CURRENT_PID

echo "2. Calling 'exec perl' (this replaces the current process)..."
echo "-----------------------------------------------------------"

# Use 'exec' to replace the shell with perl.
# This invokes the execve() syscall.
exec perl -e '
    $actual_pid = $$;
    $expected = $ENV{EXPECTED_PID};
    print "3. Perl Process PID:  $actual_pid\n";
    print "-----------------------------------------------------------\n";
    
    if ($actual_pid == $expected) {
        print "RESULT: SUCCESS - PID was preserved!\n";
        print "This proves that execve() keeps the same PID.\n";
    } else {
        print "RESULT: FAILURE - PID changed from $expected to $actual_pid\n";
    }
'
