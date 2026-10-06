package com.moviango.mediaplayer;

interface NodeSubscriptionCallback {
    public void addNodes(int[] nodes, int before);
    public void delNodes(int[] nodes);
    public void moveNode(int node, int before);
};

