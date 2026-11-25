// Runs an integration's connection test with the values typed in its form
// (nothing is saved) and shows each step's result. Server text is inserted
// with textContent only.
function runConnectionTest(button, url, resultId) {
    var form = button.closest('form');
    var result = document.getElementById(resultId);
    var label = button.innerHTML;
    button.disabled = true;
    button.innerHTML = '<span class="spinner-border spinner-border-sm"></span> Testing…';
    result.replaceChildren();

    fetch(url, {
        method: 'POST',
        body: new URLSearchParams(new FormData(form)),
        credentials: 'same-origin'
    }).then(function (r) {
        if (r.redirected || !(r.headers.get('Content-Type') || '').includes('application/json')) {
            throw new Error('Your session has expired. Reload the page and log in again.');
        }
        if (!r.ok) {
            throw new Error('The test request failed (HTTP ' + r.status + ').');
        }
        return r.json();
    }).then(function (res) {
        renderTestSteps(result, res.steps || []);
    }).catch(function (e) {
        renderTestSteps(result, [{title: 'Test', status: 'FAILED', message: e.message}]);
    }).finally(function () {
        button.disabled = false;
        button.innerHTML = label;
    });
}

function renderTestSteps(container, steps) {
    var badge = {OK: 'bg-success', WARNING: 'bg-warning text-dark', FAILED: 'bg-danger', SKIPPED: 'bg-secondary'};
    var list = document.createElement('ul');
    list.className = 'list-group';
    steps.forEach(function (s) {
        var item = document.createElement('li');
        item.className = 'list-group-item';
        var head = document.createElement('div');
        var b = document.createElement('span');
        b.className = 'badge me-2 ' + (badge[s.status] || 'bg-secondary');
        b.textContent = s.status;
        var t = document.createElement('strong');
        t.textContent = s.title;
        head.append(b, t);
        var msg = document.createElement('div');
        msg.className = 'mt-1';
        msg.textContent = s.message;
        item.append(head, msg);
        if (s.detail) {
            var d = document.createElement('details');
            var sum = document.createElement('summary');
            sum.className = 'small text-muted';
            sum.textContent = 'Details';
            var pre = document.createElement('pre');
            pre.className = 'small mb-0 mt-1';
            pre.style.whiteSpace = 'pre-wrap';
            pre.textContent = s.detail;
            d.append(sum, pre);
            item.append(d);
        }
        list.append(item);
    });
    container.append(list);
}
