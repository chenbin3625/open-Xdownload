This local Picomatch patch mitigates GHSA-fc5q-39xc-vjpf using the parser's existing
literalization guard for repeated branches containing unbounded quantifiers.
Run `npm run test:security` to verify attack inputs and ordinary matching in a
subprocess with a timeout. Version-only scanners still report version 4.0.7.
Remove the patch only after a fixed upstream version passes these checks.
