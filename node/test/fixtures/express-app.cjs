// Minimal Express app for the stdio certification: the runtime imports it, learns
// the route table and dispatches verify in-process — no listener is started.
const express = require('express');

const app = express();
app.use(express.json());
app.get('/api/orders', (_req, res) => res.json({ orders: [] }));
app.post('/api/orders', (_req, res) => res.status(201).json({ id: 1 }));

module.exports = app;
