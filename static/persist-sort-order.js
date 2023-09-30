const urlSearchParams = (new URL(document.location)).searchParams;

let sort = urlSearchParams.get("sort");
if (sort) {
    sort = sort.trim().toLowerCase();
}
const isDefaultSort = sort === "name" || sort === "created";
if (isDefaultSort) {
    document.cookie = `sort=0; Path=${location.pathname}; Max-Age=-1; SameSite=Lax;`;
} else if (sort === "name" || sort === "created" || sort === "edited" || sort === "title") {
    document.cookie = `sort=${sort}; Path=${location.pathname}; Max-Age=${60 * 60 * 24 * 365}; SameSite=Lax;`;
}

let order = urlSearchParams.get("order");
if (order) {
    order = order.trim().toLowerCase();
}
const isDefaultOrder = order === null || ((sort === "title" || sort === "name") && order === "asc") || ((sort === "created" || sort === "edited") && order === "desc");
if (isDefaultOrder) {
    document.cookie = `order=0; Path=${location.pathname}; Max-Age=-1; SameSite=Lax;`;
} else if (order === "asc" || order === "desc") {
    document.cookie = `order=${order}; Path=${location.pathname}; Max-Age=${60 * 60 * 24 * 365}; SameSite=Lax;`;
}
