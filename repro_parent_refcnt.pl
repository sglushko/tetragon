#!/usr/bin/env perl
use strict;
use warnings;
use Time::HiRes qw(time);
use IO::Socket::INET;

my $mode = shift @ARGV // 'root';

# This script is intended to stress Tetragon's process cache
# by creating complex trees of Perl processes with multiple
# fork+exec sequences and different lifetime patterns.

my $children = 8;

sub cpu_burn {
    my ($seconds) = @_;
    my $end = time + $seconds;
    my $x = 0;
    while (time < $end) {
        for (1 .. 100_000) { $x++ }
    }
}

# 1) Classic fork + wait: parent waits for child to finish.
sub scenario_classic_wait {
    my $pid = fork();
    return unless defined $pid;

    if ($pid == 0) {
        cpu_burn(0.5);
        exit 0;
    }

    waitpid($pid, 0);
}

# 2) Parent exits immediately, child continues running (orphan child).
sub scenario_orphan_child {
    my $pid = fork();
    return unless defined $pid;

    if ($pid == 0) {
        cpu_burn(1.0);
        exit 0;
    }

    # Parent exits quickly while child is still running.
    exit 0;
}

# 3) Child exits immediately, parent lives longer and does work.
sub scenario_fast_child_slow_parent {
    my $pid = fork();
    return unless defined $pid;

    if ($pid == 0) {
        exit 0;
    }

    cpu_burn(1.0);
}

# 4) fork + exec in child (exec other binary).
sub scenario_child_exec_other {
    my $pid = fork();
    return unless defined $pid;

    if ($pid == 0) {
        exec '/bin/sh', '-c', 'true';
    }
}

# 5) fork + exec in parent (re-exec into child_exec mode).
sub scenario_parent_exec_self {
    exec $^X, $0, 'child_exec';
}

# 6) Multiple execs in same PID via shell "exec".
sub scenario_multi_exec_same_pid {
    my $pid = fork();
    return unless defined $pid;

    if ($pid == 0) {
        exec '/bin/sh', '-c', 'exec /bin/true';
    }
}

# 7/9) One parent with many children, some of which exec this script
# again in a different mode, others just busy-wait.
sub scenario_chain_children {
    for (1 .. $children) {
        my $pid = fork();
        next unless defined $pid;

        if ($pid == 0) {
            if (rand() < 0.5) {
                exec $^X, $0, 'child_exec';
            }

            cpu_burn(1.0);
            exit 0;
        }
    }
}

# 8) Double-fork / daemonize-style pattern: intermediate exits,
# grandchild continues running.
sub scenario_double_fork_daemon {
    my $pid = fork();
    return unless defined $pid;

    if ($pid == 0) {
        my $gpid = fork();
        if (!defined $gpid) {
            exit 1;
        }
        if ($gpid == 0) {
            cpu_burn(0.5);
            exit 0;
        }
        # Intermediate child exits immediately, grandchild stays.
        exit 0;
    }
}

# 10) Child that performs a small UDP send to exercise network paths.
sub scenario_network {
    my $pid = fork();
    return unless defined $pid;

    if ($pid == 0) {
        my $sock = IO::Socket::INET->new(
            PeerAddr => '127.0.0.1',
            PeerPort => 9,
            Proto    => 'udp',
        );
        if ($sock) {
            $sock->send('tetragon-test');
            $sock->close();
        }
        exit 0;
    }
}

sub scenario_child_exec_mode {
    my ($children) = @_;

    # Behave like an intermediate script layer that forks more
    # children and may double-fork.
    for (1 .. int($children / 2) + 1) {
        my $pid = fork();
        next unless defined $pid;

        if ($pid == 0) {
            # Sometimes perform a double-fork inside child_exec as well.
            if (rand() < 0.3) {
                my $gpid = fork();
                if (!defined $gpid) {
                    exit 1;
                }
                if ($gpid == 0) {
                    cpu_burn(0.5);
                    exit 0;
                }
                exit 0;
            }

            cpu_burn(0.5);
            exit 0;
        }
    }
}

if ($mode eq 'root') {
    # Pick one of the scenarios above at random for this invocation.
    my @scenarios = (
        \&scenario_classic_wait,
        \&scenario_orphan_child,
        \&scenario_fast_child_slow_parent,
        \&scenario_child_exec_other,
        \&scenario_parent_exec_self,
        \&scenario_multi_exec_same_pid,
        \&scenario_chain_children,
        \&scenario_double_fork_daemon,
        \&scenario_network,
    );

    my $idx = int(rand(@scenarios));
    $scenarios[$idx]->();
}
elsif ($mode eq 'child_exec') {
    scenario_child_exec_mode($children);
}

exit 0;
