/* SPDX-License-Identifier: GPL-2.0-only */
#ifndef _ASTERISK_MDD_ADMISSION_H
#define _ASTERISK_MDD_ADMISSION_H

#include "asterisk/optional_api.h"

/* Return 1 only for an exact, fresh ALLOW from the local monotonic gate. */
AST_OPTIONAL_API(int, ast_mdd_admission_check, (const char *operation), { return 0; });

#endif /* _ASTERISK_MDD_ADMISSION_H */
