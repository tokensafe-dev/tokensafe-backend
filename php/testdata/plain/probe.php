<?php

/*
 * Plain PHP probe: no framework, no router, no classes.
 *
 * selftest.sh runs the adapter's verify default command (`php -l {file}`)
 * against this file, and `php -l` against it is the fixture's syntax baseline.
 */

function probe_value(): int
{
    return 42;
}
