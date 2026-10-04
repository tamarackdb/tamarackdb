// Package dcb holds TamarackDB's domain types, after the DCB (Dynamic
// Consistency Boundaries) specification: events, their tags, queries,
// and Append Conditions, with their validation. Packages api, store, and
// tx all share them.
//
// # Queries
//
// A Query has three forms: QueryAll, QueryNone, and a list of QueryItem.
// Items combine by OR. Within an item, the types combine by OR, and the
// identifiers, the metadata, and the three axes combine by AND. Names,
// values, and types compare as exact byte strings, with no case folding
// and no Unicode normalization.
//
// The same query runs in two places: as SQL in package store, against
// committed events, and in memory through Query.Matches, against a
// transaction's pending events. Both MUST select exactly the same events,
// or a decision could miss a pending event without any error. Both run
// the cases in testdata/query-cases.json.
//
// # Append Conditions
//
// An AppendCondition with neither a query nor an afterSequence always
// holds: it says nothing, so it protects nothing. It MUST NOT be read as
// afterSequence 0, which would fail as soon as the store holds one event.
//
// # Time
//
// An event's time is written everywhere in TimeLayout: UTC, with exactly
// six fractional digits. The store keeps it as text and orders nothing by
// it, but the fixed width keeps the text sorting the same way as the
// time. An offset, or another number of digits, would break that: "05.123Z"
// sorts after "05.123456Z", since "Z" comes after every digit.
package dcb
