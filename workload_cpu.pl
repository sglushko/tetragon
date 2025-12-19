#!/usr/bin/env perl
use strict;
use warnings;
use Time::HiRes qw(time);

my $mode = shift @ARGV // 'root';

# This script is intended to stress Tetragon's process cache
# by creating complex trees of Perl processes with multiple
# fork+exec sequences.

my $children = 8;

if ($mode eq 'root') {
    # Root mode: fork several children. Some children exec this
    # same script in "child_exec" mode, others just busy-wait.
    for (1 .. $children) {
        my $pid = fork();
        next unless defined $pid;

        if ($pid == 0) {
            if (rand() < 0.5) {
                # Exec ourselves in a different mode to create
                # additional exec transitions on the same tree.
                # Use the current Perl interpreter ($^X) so we
                # do not depend on the script file being
                # executable.
                exec $^X, $0, 'child_exec';
            }

            my $end = time + 1.0;
            my $x = 0;
            while (time < $end) {
                for (1 .. 100_000) { $x++ }
            }
            exit 0;
        }
    }

    # Occasionally exec another binary from the parent.
    if (rand() < 0.5) {
        exec '/bin/true';
    }
}
elsif ($mode eq 'child_exec') {
    # child_exec mode: behave like an intermediate script layer
    # that may fork more children and then exit while they live.
    for (1 .. int($children / 2) + 1) {
        my $pid = fork();
        next unless defined $pid;

        if ($pid == 0) {
            my $end = time + 0.5;
            my $x = 0;
            while (time < $end) {
                for (1 .. 100_000) { $x++ }
            }
            exit 0;
        }
    }
}

exit 0;
