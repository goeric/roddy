# /// script
# requires-python = ">=3.10"
# dependencies = [
#     "starlette",
#     "uvicorn",
# ]
# ///
"""Tiny demo app for exercising `roddy assert`."""

import uvicorn
from starlette.applications import Starlette
from starlette.responses import HTMLResponse
from starlette.routing import Route


async def homepage(request):
    return HTMLResponse("""<!DOCTYPE html>
<html lang="en">
<head><title>Task Tracker</title></head>
<body>
  <nav aria-label="Main">
    <a href="/">Home</a>
    <a href="/about">About</a>
  </nav>
  <main>
    <h1>Task Tracker</h1>
    <p class="subtitle">Stay on top of your work</p>
    <ul id="tasks">
      <li class="task">Write tests</li>
      <li class="task">Review PR</li>
      <li class="task">Deploy to staging</li>
    </ul>
    <span id="task-count">3</span> tasks remaining
    <div class="logged-in" data-user="alice">Logged in as alice</div>
  </main>
  <footer>© 2026 Task Tracker</footer>
</body>
</html>""")


async def about(request):
    return HTMLResponse("""<!DOCTYPE html>
<html lang="en">
<head><title>About - Task Tracker</title></head>
<body>
  <nav aria-label="Main">
    <a href="/">Home</a>
    <a href="/about">About</a>
  </nav>
  <main>
    <h1>About</h1>
    <p>A simple task tracking application.</p>
  </main>
</body>
</html>""")


app = Starlette(routes=[
    Route("/", homepage),
    Route("/about", about),
])

if __name__ == "__main__":
    uvicorn.run(app, host="127.0.0.1", port=18092)
