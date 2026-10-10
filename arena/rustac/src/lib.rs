use aho_corasick::{AhoCorasick, AhoCorasickBuilder, AhoCorasickKind, MatchKind};
use std::ffi::CString;
use std::os::raw::c_char;
use std::ptr;

pub struct Matcher {
    ac: Option<AhoCorasick>,
    ids: Vec<usize>,
    empty: Option<usize>,
}

fn set_error(out: *mut *mut c_char, message: impl std::fmt::Display) {
    if out.is_null() {
        return;
    }
    let text = CString::new(message.to_string())
        .unwrap_or_else(|_| CString::new("Rust Aho-Corasick compile failed").unwrap());
    unsafe { *out = text.into_raw() };
}

fn input_pattern<'a>(
    patterns: *const *const u8,
    lengths: *const usize,
    index: usize,
) -> Result<&'a [u8], &'static str> {
    let length = unsafe { *lengths.add(index) };
    let bytes = unsafe { *patterns.add(index) };
    if length == 0 {
        return Ok(&[]);
    }
    if bytes.is_null() {
        return Err("null non-empty pattern");
    }
    Ok(unsafe { std::slice::from_raw_parts(bytes, length) })
}

#[no_mangle]
pub extern "C" fn casei_ac_compile(
    patterns: *const *const u8,
    lengths: *const usize,
    count: usize,
    error: *mut *mut c_char,
) -> *mut Matcher {
    if !error.is_null() {
        unsafe { *error = ptr::null_mut() };
    }
    if count != 0 && (patterns.is_null() || lengths.is_null()) {
        set_error(error, "null pattern array");
        return ptr::null_mut();
    }

    let mut empty = None;
    let mut ids = Vec::new();
    let mut literals = Vec::new();
    for index in 0..count {
        let bytes = match input_pattern(patterns, lengths, index) {
            Ok(bytes) => bytes,
            Err(message) => {
                set_error(error, message);
                return ptr::null_mut();
            }
        };
        if !bytes.is_ascii() {
            set_error(error, "Rust Aho-Corasick entrant is ASCII-only");
            return ptr::null_mut();
        }
        if bytes.is_empty() {
            empty = Some(empty.map_or(index, |old: usize| old.min(index)));
        } else {
            ids.push(index);
            literals.push(bytes.to_vec());
        }
    }

    let ac = if literals.is_empty() {
        None
    } else {
        match AhoCorasickBuilder::new()
            .ascii_case_insensitive(true)
            .match_kind(MatchKind::LeftmostFirst)
            .kind(Some(AhoCorasickKind::DFA))
            .prefilter(true)
            .build(literals)
        {
            Ok(ac) => Some(ac),
            Err(message) => {
                set_error(error, message);
                return ptr::null_mut();
            }
        }
    };

    Box::into_raw(Box::new(Matcher { ac, ids, empty }))
}

#[no_mangle]
pub unsafe extern "C" fn casei_ac_free(matcher: *mut Matcher) {
    if !matcher.is_null() {
        drop(Box::from_raw(matcher));
    }
}

#[no_mangle]
pub unsafe extern "C" fn casei_ac_error_free(error: *mut c_char) {
    if !error.is_null() {
        drop(CString::from_raw(error));
    }
}

#[no_mangle]
pub unsafe extern "C" fn casei_ac_find(
    matcher: *const Matcher,
    haystack: *const u8,
    length: usize,
    start: *mut usize,
    pattern: *mut usize,
    dispatch_bits: *mut u32,
) -> i32 {
    if !dispatch_bits.is_null() {
        *dispatch_bits = 0;
    }
    if matcher.is_null()
        || start.is_null()
        || pattern.is_null()
        || (length != 0 && haystack.is_null())
    {
        return -1;
    }
    let matcher = &*matcher;
    let mut best = matcher.empty.map(|id| (0, id));
    if let Some(ac) = &matcher.ac {
        let haystack = if length == 0 {
            &[]
        } else {
            std::slice::from_raw_parts(haystack, length)
        };
        // The patched, pinned memchr dependency records only work reached by
        // this search. Keeping reset, find, and readback in this native call
        // preserves the thread-local observation across the cgo boundary.
        memchr::casei_dispatch_reset();
        let found = ac.find(haystack);
        if !dispatch_bits.is_null() {
            *dispatch_bits = memchr::casei_dispatch_bits() as u32;
        }
        if let Some(found) = found {
            let candidate = (found.start(), matcher.ids[found.pattern().as_usize()]);
            if best.map_or(true, |old| candidate < old) {
                best = Some(candidate);
            }
        }
    }
    match best {
        Some((found_start, found_pattern)) => {
            *start = found_start;
            *pattern = found_pattern;
            1
        }
        None => 0,
    }
}

/// Writes up to `cap` (start, end, pattern) triples of consecutive
/// non-overlapping matches from `start` into `out`, from find_iter, and returns
/// how many it wrote, or -1 on bad input or an empty pattern.
#[no_mangle]
pub unsafe extern "C" fn casei_ac_each(
    matcher: *const Matcher,
    haystack: *const u8,
    length: usize,
    start: usize,
    out: *mut usize,
    cap: usize,
) -> isize {
    if matcher.is_null() || out.is_null() || (length != 0 && haystack.is_null()) || start > length {
        return -1;
    }
    let matcher = &*matcher;
    if matcher.empty.is_some() {
        return -1;
    }
    let Some(ac) = &matcher.ac else { return 0 };
    let haystack = if length == 0 { &[][..] } else { std::slice::from_raw_parts(haystack, length) };
    let out = std::slice::from_raw_parts_mut(out, 3 * cap);
    let mut n = 0;
    for found in ac.find_iter(aho_corasick::Input::new(haystack).span(start..length)).take(cap) {
        out[3 * n] = found.start();
        out[3 * n + 1] = found.end();
        out[3 * n + 2] = matcher.ids[found.pattern().as_usize()];
        n += 1;
    }
    n as isize
}
