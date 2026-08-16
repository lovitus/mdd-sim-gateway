# AOSP Carrier ID data

`carrier_list.textpb` is copied from Android Open Source Project's
`platform/packages/providers/TelephonyProvider` repository and is licensed under
Apache-2.0. It is used offline to identify a SIM's home network and, when SPN/GID/IMSI
rules match, its specific MVNO brand.

Vendored revision: `bca387f553a4493c88e24455172225fd1049c91f`

Refresh deliberately, after reviewing the upstream diff:

```bash
python3 tools/update_aosp_carrier_data.py <full-reviewed-commit>
```
