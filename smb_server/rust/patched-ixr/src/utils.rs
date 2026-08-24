//! Small helpers shared across modules.

use std::time::{SystemTime, UNIX_EPOCH};

/// Number of 100-nanosecond intervals between 1601-01-01 (Windows FILETIME
/// epoch) and 1970-01-01 (UNIX epoch). 369 years.
const FILETIME_OFFSET: u64 = 116_444_736_000_000_000;

/// Convert a `SystemTime` to a Windows FILETIME (100ns ticks since 1601).
pub fn system_time_to_filetime(t: SystemTime) -> u64 {
    match t.duration_since(UNIX_EPOCH) {
        Ok(d) => FILETIME_OFFSET + (d.as_secs() * 10_000_000) + (d.subsec_nanos() as u64 / 100),
        // Pre-1970 — clamp to the FILETIME epoch.
        Err(_) => 0,
    }
}

/// Convert "now" to FILETIME.
pub fn now_filetime() -> u64 {
    system_time_to_filetime(SystemTime::now())
}

/// Encode a `&str` to little-endian UTF-16 bytes.
pub fn utf16le(s: &str) -> Vec<u8> {
    let mut out = Vec::with_capacity(s.len() * 2);
    for unit in s.encode_utf16() {
        out.extend_from_slice(&unit.to_le_bytes());
    }
    out
}

/// Decode a UTF-16LE byte slice. Returns an empty string if the buffer is not
/// 2-byte aligned (caller decides what to do); replacement characters on
/// invalid surrogates.
pub fn utf16le_to_string(bytes: &[u8]) -> String {
    if !bytes.len().is_multiple_of(2) {
        return String::new();
    }
    let units: Vec<u16> = bytes
        .chunks_exact(2)
        .map(|c| u16::from_le_bytes([c[0], c[1]]))
        .collect();
    String::from_utf16_lossy(&units)
}

/// Decode a UTF-16LE byte slice into a `Vec<u16>`, returning `None` on a
/// non-aligned buffer.
pub fn utf16le_to_units(bytes: &[u8]) -> Option<Vec<u16>> {
    if !bytes.len().is_multiple_of(2) {
        return None;
    }
    Some(
        bytes
            .chunks_exact(2)
            .map(|c| u16::from_le_bytes([c[0], c[1]]))
            .collect(),
    )
}

/// Fill `out` with cryptographically-strong random bytes via `getrandom`.
/// Falls back to zeros if the OS RNG fails — the caller should treat this as
/// fatal, but we never panic.
pub fn fill_random(out: &mut [u8]) {
    if getrandom::fill(out).is_err() {
        for b in out.iter_mut() {
            *b = 0;
        }
    }
}

/// DOS-style glob matching used by QUERY_DIRECTORY pattern filtering.
///
/// `?` matches any single char, `*` matches any sequence (possibly empty).
/// Comparison is case-insensitive (ASCII fold) — sufficient for names that
/// are validated to be free of weird Unicode tricks.
pub fn glob_match(pattern: &str, name: &str) -> bool {
    // Walk both strings as char vectors so `?` matches a char rather than a
    // byte, without going through grapheme territory.
    let p: Vec<char> = pattern.chars().collect();
    let n: Vec<char> = name.chars().collect();
    glob_match_inner(&p, &n)
}

fn glob_match_inner(p: &[char], n: &[char]) -> bool {
    let mut pi = 0usize;
    let mut ni = 0usize;
    let mut star: Option<(usize, usize)> = None; // (pi after '*', ni at the time)

    while ni < n.len() {
        if pi < p.len() && (p[pi] == '?' || ascii_eq_ci(p[pi], n[ni])) {
            pi += 1;
            ni += 1;
        } else if pi < p.len() && p[pi] == '*' {
            star = Some((pi + 1, ni));
            pi += 1;
        } else if let Some((sp, sn)) = star {
            pi = sp;
            ni = sn + 1;
            star = Some((sp, sn + 1));
        } else {
            return false;
        }
    }
    while pi < p.len() && p[pi] == '*' {
        pi += 1;
    }
    pi == p.len()
}

fn ascii_eq_ci(a: char, b: char) -> bool {
    a.eq_ignore_ascii_case(&b)
}

/// True if `pattern` matches every name (DOS-speak: empty, `*`, `*.*`).
pub fn glob_match_all(pattern: &str) -> bool {
    pattern.is_empty() || pattern == "*" || pattern == "*.*"
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn glob_match_basics() {
        assert!(glob_match("*", "anything"));
        assert!(glob_match("*.txt", "foo.txt"));
        assert!(!glob_match("*.txt", "foo.log"));
        assert!(glob_match("a?c", "abc"));
        assert!(!glob_match("a?c", "ac"));
        assert!(glob_match("a*b*c", "axxxbxxxc"));
        assert!(glob_match("FOO", "foo"));
        assert!(glob_match("", ""));
        assert!(!glob_match("", "a"));
        assert!(glob_match_all(""));
        assert!(glob_match_all("*"));
        assert!(glob_match_all("*.*"));
        assert!(!glob_match_all("*.txt"));
    }
}
