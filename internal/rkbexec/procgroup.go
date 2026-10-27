package rkbexec

import "time"

// groupGrace is how long processes that outlived the direct child get to exit
// after the tree is signalled, before they are killed. Together with waitDelay
// the whole stop is about 3s.
const groupGrace = time.Second
