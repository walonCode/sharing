package transfer

import "syscall"

// syscallNoFollow makes open fail if the final path component is a symlink.
const syscallNoFollow = syscall.O_NOFOLLOW
