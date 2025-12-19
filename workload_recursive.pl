#!/usr/bin/env perl
use strict;
use warnings;
use Time::HiRes qw(sleep);

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

chain_long_parent($chain_depth);

exit 0;
