/* eslint-disable no-param-reassign */

// Bulk actions "by query" (Select all, then delete, blocklist or change lists)
// reach every row matching a query, and an empty query reaches the whole table.
// So they must target the query of the rows on screen, not the live search box.
// A view using these helpers has `appliedQuery` (null until its first response),
// `querySeq` (0) and `bulk` ({ checked, all }) in its data.

export const sameQuery = (a, b) => JSON.stringify(a) === JSON.stringify(b);

// Call when a list request starts. Returns the function to call in that
// request's `.then`, which does nothing unless it is still the view's newest
// request. An older response or a failure therefore leaves the rows and
// `appliedQuery` as they were, so the two cannot disagree.
export const beginQuery = (view, query) => {
  view.querySeq += 1;
  const seq = view.querySeq;

  return (applyRows) => {
    if (seq !== view.querySeq) {
      return;
    }
    if (applyRows) {
      applyRows();
    }

    // A selection made over one query says nothing about another.
    if (!sameQuery(view.appliedQuery, query)) {
      view.bulk.all = false;
      view.bulk.checked = [];
    }
    view.appliedQuery = query;
  };
};

// True if a by-query action may go ahead on `target`, a value of `appliedQuery`
// captured when the action started. Check it when the action starts and again
// after any confirmation. Otherwise it tells the user nothing was changed.
export const canActOnQuery = (view, target) => {
  if (target !== null && sameQuery(target, view.appliedQuery)) {
    return true;
  }
  view.$utils.toast(view.$t('globals.messages.bulkTargetStale'), 'is-danger');
  return false;
};
