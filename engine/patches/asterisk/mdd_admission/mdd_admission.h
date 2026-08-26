/* SPDX-License-Identifier: GPL-2.0-only */
#ifndef _ASTERISK_MDD_ADMISSION_H
#define _ASTERISK_MDD_ADMISSION_H

#include "asterisk/optional_api.h"

/* Return 1 only for an exact, fresh ALLOW from the local monotonic gate. */
AST_OPTIONAL_API(int, ast_mdd_admission_check, (const char *operation), { return 0; });
/* Local recovery fence presence is authoritative even when its JSON is corrupt. */
AST_OPTIONAL_API(int, ast_mdd_registration_fenced, (void), { return 1; });
/* Consume one request-owned permit_nonce; receipt durability precedes ALLOW. */
AST_OPTIONAL_API(int, ast_mdd_registration_begin, (const char *permit_nonce), { return -1; });
AST_OPTIONAL_API(void, ast_mdd_registration_end, (int handle), { (void) handle; });

#endif /* _ASTERISK_MDD_ADMISSION_H */
