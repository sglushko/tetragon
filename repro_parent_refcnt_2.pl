#!/usr/bin/env perl
use strict;
use warnings;
use Time::HiRes qw(sleep);

my $mode = shift @ARGV // 'root';

# This script focuses on long-lived parent/child lifetimes
# and deep recursive process trees (dozens of levels).
# Long/short lifetimes and chain depth are configurable via
# environment variables:
#   LONG_LIFETIME_SEC  - how long "long" processes live (default 180)
#   SHORT_LIFETIME_SEC - how long "short" processes live (default 1)
#   CHAIN_DEPTH        - depth of recursive chains (default 32)

my $long_lifetime  = $ENV{LONG_LIFETIME_SEC}  // 180;
my $short_lifetime = $ENV{SHORT_LIFETIME_SEC} // 1;
my $chain_depth    = $ENV{CHAIN_DEPTH}        // 10;

sub long_sleep {
    my ($sec) = @_;
    sleep($sec);
}

# Simple pair: parent lives much longer than child.
sub scenario_long_parent_short_child {
    my $pid = fork();
    return unless defined $pid;

    if ($pid == 0) {
        long_sleep($short_lifetime);
        exit 0;
    } else {
        long_sleep($long_lifetime);
        exit 0;
    }
}

# Simple pair: child lives much longer than parent.
sub scenario_short_parent_long_child {
    my $pid = fork();
    return unless defined $pid;

    if ($pid == 0) {
        long_sleep($long_lifetime);
        exit 0;
    } else {
        long_sleep($short_lifetime);
        exit 0;
    }
}

# Deep chain where at each level the parent is long-lived
# and the child recurses to create the next level.
sub chain_long_parent {
    my ($depth) = @_;

    if ($depth <= 0) {
        long_sleep($short_lifetime);
        exit 0;
    }

    my $pid = fork();
    return unless defined $pid;

    if ($pid == 0) {
        chain_long_parent($depth - 1);
        exit 0;
    } else {
        long_sleep($long_lifetime);
        exit 0;
    }
}

# Deep chain where at each level the parent exits quickly
# and the child stays around for a long time and recurses.
sub chain_long_child {
    my ($depth) = @_;

    if ($depth <= 0) {
        long_sleep($long_lifetime);
        exit 0;
    }

    my $pid = fork();
    return unless defined $pid;

    if ($pid == 0) {
        long_sleep($long_lifetime);
        chain_long_child($depth - 1);
        exit 0;
    } else {
        long_sleep($short_lifetime);
        exit 0;
    }
}

if ($mode eq 'root') {
    # For each invocation pick one of the scenarios at random.
    my @scenarios = (
        \&scenario_long_parent_short_child,
        \&scenario_short_parent_long_child,
        sub { chain_long_parent($chain_depth) },
        sub { chain_long_child($chain_depth) },
    );

    my $idx = int(rand(@scenarios));
    $scenarios[$idx]->();
}
elsif ($mode eq 'deep_parent') {
    my $depth = shift @ARGV // $chain_depth;
    chain_long_parent($depth);
}
elsif ($mode eq 'deep_child') {
    my $depth = shift @ARGV // $chain_depth;
    chain_long_child($depth);
}

exit 0;
